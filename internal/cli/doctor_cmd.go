package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/output"
	"github.com/spf13/cobra"
)

// The doctor exists because a failure in CERNBox usually arrives as a status
// code with nothing attached to it. A storage that has stopped accepting writes
// answers an upload with 500; a clock an hour out of step makes Kerberos say
// "authentication failed"; a CERNBOX_CONFIG pointing at a file that is not
// there is ignored in silence. Each of those took real time to work out by
// hand, and each is one request to check.
//
// So this command does not report state — "cernbox status" does that. It tries
// the things that go wrong, in the order they depend on each other, and names
// what to do about the ones that did.
//
// One thing it deliberately does not check is versioning, and the reason is
// worth writing down because the check is tempting: a file written twice, its
// history listed and one revision read back, catches a deployment that offers a
// history it cannot serve. What it cannot do is tell that apart from a space
// that is not supposed to keep versions at all. Spaces are becoming
// heterogeneous — capabilities per space rather than per server — so absence of
// versioning will be a correct answer for some of them, and the only flag this
// command can see is the server-wide one. A check whose failure reading is
// wrong for a supported configuration is worse than no check.

const (
	// doctorTrashDays is how far back the trash check looks. The bin is listed
	// one day per request, so this is a direct cost, and a week is enough to
	// find a day the storage will not list without making the doctor slow.
	doctorTrashDays = 7

	// doctorClockWarn and doctorClockFail are how far the two clocks may drift.
	// The hard limit is Kerberos's: it refuses a ticket more than five minutes
	// out of step, and the message it gives back says nothing about clocks.
	doctorClockWarn = time.Minute
	doctorClockFail = 5 * time.Minute

	// doctorClockResolution is as precisely as the two clocks can be compared at
	// all: an HTTP Date carries whole seconds, so a perfectly set machine still
	// reads as up to a second out. Reporting that would be a number that never
	// goes away and never means anything.
	doctorClockResolution = time.Second

	// doctorQuotaWarn is the share of a quota in use that is worth mentioning.
	doctorQuotaWarn = 90

	// doctorProbeBody is what the write probe writes.
	doctorProbeBody = "cernbox doctor: write probe\n"
)

func newDoctorCmd(app *App) *cobra.Command {
	var skipWrite bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check that everything the client depends on works",
		Long: "Try the things that go wrong, and say what to do about the ones that did.\n" +
			"\n" +
			"Each check is one or two requests: the server answers, the clock agrees, the\n" +
			"credential works, a file can actually be written. Checks stop at the first\n" +
			"failure they depend on, so a server nobody can reach is reported once rather\n" +
			"than as ten mysterious failures.\n" +
			"\n" +
			"The output is meant to be pasted into a support request. It names providers,\n" +
			"expiry times and paths, and never a token or a password.\n" +
			"\n" +
			"Only the write check changes anything: it puts one small file in your home\n" +
			"space and removes it again. --skip-write leaves it out.",
		Example: "  cernbox doctor\n" +
			"  cernbox doctor --skip-write\n" +
			"  cernbox --output json doctor | jq .problems",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := app.ctx(cmd)
			defer cancel()
			return app.runDoctor(ctx, skipWrite)
		},
	}

	cmd.Flags().BoolVar(&skipWrite, "skip-write", false,
		"do not write a probe file, so nothing is created even briefly")
	return cmd
}

// ── the report ───────────────────────────────────────────────────────────────

// checkStatus is one check's verdict. "skipped" is a real answer and not a
// failure: a check that could not be attempted must say so rather than guess,
// because a doctor that guesses is worse than no doctor.
type checkStatus string

const (
	checkOK   checkStatus = "ok"
	checkWarn checkStatus = "warning"
	checkFail checkStatus = "problem"
	checkSkip checkStatus = "skipped"
)

// doctorCheck is one line of the report.
type doctorCheck struct {
	Name   string      `json:"name"`
	Status checkStatus `json:"status"`
	Detail string      `json:"detail,omitempty"`
	// Remedy is what to do about it, and is the point of the whole command: a
	// check that reports a state without naming the next step has only moved the
	// confusion somewhere else. It is empty when there is nothing to do.
	Remedy string `json:"remedy,omitempty"`
}

// doctorReport is the whole run. Problems and Warnings are counted here rather
// than left to the reader so that a monitoring probe can be one jq expression.
type doctorReport struct {
	Checks   []doctorCheck `json:"checks"`
	Problems int           `json:"problems"`
	Warnings int           `json:"warnings"`
}

// doctorRun accumulates the report and carries what one check learns to the
// next: the home space the storage checks need, and the probe file the version
// check reads back.
type doctorRun struct {
	app       *App
	ctx       context.Context
	skipWrite bool

	checks []doctorCheck

	// serverTime is the clock the endpoint check read, reused by the clock check
	// rather than asked for a second time.
	serverTime time.Time

	home  *client.Space
	probe string
}

func (r *doctorRun) add(name string, status checkStatus, detail, remedy string) {
	r.checks = append(r.checks, doctorCheck{Name: name, Status: status, Detail: detail, Remedy: remedy})
}

func (r *doctorRun) ok(name, detail string)           { r.add(name, checkOK, detail, "") }
func (r *doctorRun) skip(name, detail string)         { r.add(name, checkSkip, detail, "") }
func (r *doctorRun) warn(name, detail, remedy string) { r.add(name, checkWarn, detail, remedy) }
func (r *doctorRun) fail(name, detail, remedy string) { r.add(name, checkFail, detail, remedy) }

// runDoctor runs every check and renders the report.
func (a *App) runDoctor(ctx context.Context, skipWrite bool) error {
	r := &doctorRun{app: a, ctx: ctx, skipWrite: skipWrite}
	r.run()
	return a.renderDoctor(r.report())
}

// run works down the checks, stopping when something they all depend on has
// failed.
//
// The stopping is the design. Without it, an endpoint nobody can reach produces
// a wall of failures that all say the same thing, and the reader has to work out
// which one is the cause — which is the job this command is supposed to do for
// them.
func (r *doctorRun) run() {
	if !r.checkConfig() {
		return
	}
	// Connecting is deferred until here, rather than done by the command tree
	// before this command runs, so that a broken configuration is reported as a
	// check rather than stopping the doctor from starting.
	if err := r.app.connect(); err != nil {
		r.fail("endpoint", errLine(err),
			"the endpoint could not be set up; pass --endpoint or set CERNBOX_ENDPOINT")
		return
	}
	if !r.checkServer() {
		return
	}
	r.checkClock()
	if !r.checkCredentials() {
		return
	}
	if !r.checkIdentity() {
		return
	}

	// The storage checks all need somewhere to look.
	if r.checkSpaces() {
		if r.checkWrite() {
			r.cleanUpProbe()
		}
		r.checkQuota()
		r.checkTrash()
	}

	// Local checks, which need no server and so run whatever the storage said.
	r.checkOutbox()
	r.checkEditor()
}

// ── the checks ───────────────────────────────────────────────────────────────

// checkConfig reports which configuration files were read.
//
// The warning it exists for is CERNBOX_CONFIG: a value pointing at a file that
// is not there is ignored without a word, so the setting a user is certain they
// changed has no effect and nothing anywhere says why.
func (r *doctorRun) checkConfig() bool {
	explicit := r.app.flags.configPath
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			r.fail("config", fmt.Sprintf("%s cannot be read: %v", explicit, errLine(err)),
				"--config names the only file that is read, so it has to exist")
			return false
		}
	}

	detail := strings.Join(configFilesRead(explicit), ", ")
	if detail == "" {
		detail = "none; using the built-in defaults"
	}

	// A parse error is worth stopping for: every setting below would be the
	// default, and reporting those as if they were the user's would be a lie.
	if _, err := LoadConfig(explicit); err != nil {
		r.fail("config", errLine(err), "fix the file, or move it aside to fall back to the defaults")
		return false
	}

	if env := os.Getenv("CERNBOX_CONFIG"); env != "" && explicit == "" && !isRegularFile(env) {
		r.warn("config", detail,
			fmt.Sprintf("CERNBOX_CONFIG points at %s, which does not exist, so it was ignored", env))
		return true
	}
	r.ok("config", detail)
	return true
}

// checkServer reports whether the endpoint answers at all.
//
// It asks for nothing but the response headers and sends no credentials, so it
// separates the two failures that look identical from inside a command: a
// server that cannot be reached, and a server that will not accept who you are.
func (r *doctorRun) checkServer() bool {
	t, err := r.app.client.ServerTime(r.ctx)
	if err != nil {
		remedy := "check the address, and whether this machine needs the CERN network to reach it"
		if certificateProblem(err) {
			remedy = "the certificate was not accepted, so nothing about the address or the " +
				"network is necessarily wrong: check the certificates this machine trusts, " +
				"or SSL_CERT_FILE if the instance has its own"
		}
		r.fail("endpoint", fmt.Sprintf("%s: %s", r.app.cfg.Endpoint, errLine(err)), remedy)
		return false
	}
	r.serverTime = t

	detail := r.app.cfg.Endpoint
	if caps, err := r.app.client.Capabilities(r.ctx); err == nil && caps.ServerVersion != "" {
		detail += ", " + strings.TrimSpace(caps.ProductName+" "+caps.ServerVersion)
	}
	r.ok("endpoint", detail)
	return true
}

// checkClock compares the two clocks.
//
// Nothing else in the CLI reports this, and it is the cause behind several
// failures that name something else: Kerberos refuses a ticket more than five
// minutes out of step, and a token that has not expired looks expired to a
// client whose clock has run ahead.
func (r *doctorRun) checkClock() {
	if r.serverTime.IsZero() {
		r.skip("clock", "the server sent no usable Date header")
		return
	}
	skew := time.Since(r.serverTime)
	detail := clockDetail(skew)

	switch {
	case skew.Abs() <= doctorClockResolution:
		r.ok("clock", "in step with the server")
	case skew.Abs() >= doctorClockFail:
		r.fail("clock", detail,
			"Kerberos refuses a ticket more than five minutes out of step; set this machine's clock")
	case skew.Abs() >= doctorClockWarn:
		r.warn("clock", detail, "credentials start failing once this passes five minutes")
	default:
		r.ok("clock", detail)
	}
}

// checkCredentials reports which credential is in force, and whether it would
// still work with nobody watching.
//
// The unattended part is the failure this is here for: the device flow prints a
// URL and waits for somebody to visit it, so a command that works in a terminal
// fails from a timer with an error about nothing in particular. Which answer is
// a problem depends on where the doctor is being run — from a terminal it is
// something to know about later, from a timer it is the reason nothing works.
func (r *doctorRun) checkCredentials() bool {
	var available []string
	for _, p := range r.app.chain.Providers() {
		if p.Available(r.ctx) {
			available = append(available, p.Name())
		}
	}

	tok, err := r.app.chain.Token(r.ctx)
	if err != nil {
		detail := errLine(err)
		if len(available) > 0 {
			detail += " (tried: " + strings.Join(available, ", ") + ")"
		}
		r.fail("credentials", detail, "run 'cernbox login', or 'cernbox login --method device' to choose")
		return false
	}

	detail := tok.Provider
	if e := expiryString(tok); e != "" {
		detail += ", expires " + e
	}

	unattended := r.app.unattendedProvider(r.ctx)
	if unattended == "" {
		if !r.app.hasTerminal() {
			r.fail("credentials", detail+" — needs a terminal, and there is none",
				"give the job a Kerberos ticket, or an app token in CERNBOX_APP_TOKEN")
			return true
		}
		r.ok("credentials", detail+" — needs a terminal to renew")
		return true
	}
	if unattended == tok.Provider {
		detail += " — works with nobody present"
	} else {
		detail += "; " + unattended + " also works with nobody present"
	}
	r.ok("credentials", detail)
	return true
}

// checkIdentity reports who the server says you are, which is not the same
// question as whether a credential could be obtained: a token the client is
// perfectly happy with can still be one the server rejects.
func (r *doctorRun) checkIdentity() bool {
	me, err := r.app.client.Me(r.ctx)
	if err != nil {
		r.fail("identity", errLine(err),
			"the server would not say who this credential belongs to; 'cernbox logout' then sign in again")
		return false
	}
	detail := me.Username
	if me.DisplayName != "" {
		detail += " (" + me.DisplayName + ")"
	}
	r.ok("identity", detail)
	return true
}

// checkSpaces finds the personal space the storage checks below need.
func (r *doctorRun) checkSpaces() bool {
	spaces, err := r.app.client.Spaces(r.ctx)
	if err != nil {
		r.fail("spaces", errLine(err), "the spaces listing failed, so nothing below it could be checked")
		return false
	}
	for i := range spaces {
		if spaces[i].Type == "personal" {
			r.home = &spaces[i]
			break
		}
	}
	detail := fmt.Sprintf("%d reachable", len(spaces))
	if r.home == nil {
		r.fail("spaces", detail+", none of them yours",
			"your home space is not there; ask CERNBox support to provision it")
		return false
	}
	r.ok("spaces", fmt.Sprintf("%s, home at %s", detail, r.home.Path))
	return true
}

// checkWrite writes a file and reports whether the storage took it.
//
// This is the check that matters most, and the one nothing else can stand in
// for. A storage can be reachable, a credential valid, a quota half empty, and
// writes still refused — EOS stops accepting them when the underlying disks run
// short of headroom, and every upload then fails with a 500 that explains
// nothing. A quota reading would have said everything was fine.
func (r *doctorRun) checkWrite() bool {
	if r.skipWrite {
		r.skip("write", "not attempted (--skip-write)")
		return false
	}

	// A name nobody will wonder about if it is ever left behind, with the time
	// and the process in it so that two doctors running at once cannot collide.
	r.probe = path.Join(r.home.Path,
		fmt.Sprintf(".cernbox-doctor-%s-%d", time.Now().Format("20060102-150405"), os.Getpid()))

	if err := r.putProbe(doctorProbeBody); err != nil {
		r.probe = ""
		r.fail("write", errLine(err), writeRemedy(err))
		return false
	}
	r.ok("write", fmt.Sprintf("wrote and removed %d bytes in %s", len(doctorProbeBody), r.home.Path))
	return true
}

// writeRemedy says what to do about a refused write.
//
// Worth telling apart, because the default answer is "this is not yours to
// fix" and that is wrong in the two cases where it is: a full space, and a
// space you may not write to. Both arrive as their own status rather than as
// the unexplained 500 a storage out of headroom gives.
func writeRemedy(err error) string {
	switch cberr.KindOf(err) {
	case cberr.KindConflict:
		return "the space may be full: 'cernbox quota' says how full, and " +
			"'cernbox du --top 20' where it went"
	case cberr.KindPermission:
		return "you are not allowed to write here, which is a permission on the space itself"
	default:
		return "the storage would not take a small file, which is not something you can fix from here"
	}
}

// checkQuota reports how full the space is. Being out of quota is a problem
// rather than a warning, because nothing can be written until it is dealt with.
func (r *doctorRun) checkQuota() {
	s := *r.home
	if s.QuotaTotal <= 0 {
		r.ok("quota", "no quota set on "+client.SpaceAlias(s))
		return
	}
	pct := int64(float64(s.QuotaUsed) / float64(s.QuotaTotal) * 100)
	detail := fmt.Sprintf("%d%% of %s used in %s",
		pct, output.HumanSize(s.QuotaTotal), client.SpaceAlias(s))

	switch {
	case s.QuotaRemaining <= 0:
		r.fail("quota", detail, "nothing can be written until something is deleted and purged "+
			"from the trash; 'cernbox du --top 20' shows where it went")
	case pct >= doctorQuotaWarn:
		r.warn("quota", detail, "'cernbox du --top 20' shows where it went, and "+
			"'cernbox quota --versions' how much of it is earlier versions")
	default:
		r.ok("quota", detail)
	}
}

// checkTrash reports whether the recycle bin can be listed.
//
// The bin is listed a day at a time, and the storage refuses a day holding more
// deletions than it will return at once. A listing that hits one comes back
// short, and without this nothing says that anything is missing from it.
func (r *doctorRun) checkTrash() {
	now := time.Now()
	listing, err := r.app.client.ListTrash(r.ctx, r.home.Path, client.TrashWindow{
		From: now.AddDate(0, 0, -doctorTrashDays),
		To:   now,
	})
	if err != nil {
		r.fail("trash", errLine(err), "'cernbox trash list' will fail the same way")
		return
	}
	detail := fmt.Sprintf("%s deleted in the last %d days",
		plural(len(listing.Items), "item", "items"), doctorTrashDays)
	if n := len(listing.Gaps); n > 0 {
		r.warn("trash", fmt.Sprintf("%s, and %s could not be listed",
			detail, plural(n, "day", "days")),
			"those days hold more deletions than the storage will return at once; "+
				"ask for a narrower range, as in 'cernbox trash list --since 1d'")
		return
	}
	r.ok("trash", detail)
}

// checkOutbox reports whether the configured outbox folders are there.
//
// A folder that is not is skipped with a warning by "outbox push", which is the
// right thing for it to do and easy to never see when it runs from a timer.
func (r *doctorRun) checkOutbox() {
	if len(r.app.cfg.Outbox) == 0 {
		r.skip("outbox", "none configured")
		return
	}

	var missing []string
	for _, f := range r.app.cfg.Outbox {
		if !isDirectory(expandHome(f.Local)) {
			missing = append(missing, f.Local)
		}
	}
	detail := plural(len(r.app.cfg.Outbox), "folder", "folders") + " configured"
	if len(missing) > 0 {
		r.warn("outbox", fmt.Sprintf("%s, %s not there: %s",
			detail, plural(len(missing), "one", "several"), strings.Join(missing, ", ")),
			"'outbox push' skips a folder that is not there; create it or remove it from the configuration")
		return
	}
	r.ok("outbox", detail+", all present")
}

// checkEditor reports whether the editor "cernbox edit" would run is on PATH.
//
// Nothing else finds this out until the editor is started, which is after the
// file has been downloaded and after the session has been set up.
func (r *doctorRun) checkEditor() {
	command, source := r.app.editorSetting("")
	name := strings.Fields(command)[0]
	if _, err := exec.LookPath(name); err != nil {
		r.warn("editor", fmt.Sprintf("%s (from %s) is not on PATH", name, source),
			"'cernbox edit' will fail once it has the file; set it to something installed")
		return
	}
	r.ok("editor", fmt.Sprintf("%s (from %s)", command, source))
}

// ── the probe file ───────────────────────────────────────────────────────────

func (r *doctorRun) putProbe(body string) error {
	open := func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(body)), nil }
	return r.app.client.Upload(r.ctx, r.probe, open, int64(len(body)), "")
}

// cleanUpProbe removes the probe file and purges it from the recycle bin.
//
// A doctor that leaves litter behind is a doctor people stop running, and
// deleting is not enough on its own: a deletion goes to the bin, so a daily
// check would quietly add a year of entries to the one place users already
// complain about. Anything it fails to clear is reported with its path rather
// than swallowed, so it can be dealt with by hand.
func (r *doctorRun) cleanUpProbe() {
	if r.probe == "" {
		return
	}
	// A fresh context: the probe has to be removed even when the run was
	// interrupted, which is precisely when the command's own context is done.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.ctx), 30*time.Second)
	defer cancel()

	if err := r.app.client.Remove(ctx, r.probe); err != nil {
		r.warn("cleanup", fmt.Sprintf("%s could not be removed: %s", r.probe, errLine(err)),
			"remove it by hand with 'cernbox rm', and purge it from the trash")
		return
	}
	name := path.Base(r.probe)
	r.probe = ""

	if err := r.purgeProbe(ctx, name); err != nil {
		r.warn("cleanup", fmt.Sprintf("%s is in the recycle bin: %s", name, errLine(err)),
			"remove it with 'cernbox trash purge', or leave it to expire")
	}
}

// purgeProbe takes the probe out of the recycle bin.
//
// Matched by exact name inside the last day, and on nothing else: the name
// carries the time and the process id, so it cannot be confused with a file
// somebody cares about, and a purge cannot be undone.
//
// Every match is purged, not the first. One deletion can put several entries in
// the bin under the same name — a file with versions leaves one per version,
// measured — so stopping at the first is not enough to be sure nothing is left.
func (r *doctorRun) purgeProbe(ctx context.Context, name string) error {
	now := time.Now()
	listing, err := r.app.client.ListTrash(ctx, r.home.Path, client.TrashWindow{
		From: now.AddDate(0, 0, -1),
		To:   now,
	})
	if err != nil {
		return err
	}
	// Nothing to purge is not a failure: a storage need not put a deletion in a
	// bin at all.
	var firstErr error
	for _, it := range listing.Items {
		if it.Name != name {
			continue
		}
		if err := r.app.client.PurgeTrash(ctx, it.Key, r.home.Path); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ── rendering ────────────────────────────────────────────────────────────────

func (r *doctorRun) report() doctorReport {
	rep := doctorReport{Checks: r.checks}
	for _, c := range r.checks {
		switch c.Status {
		case checkFail:
			rep.Problems++
		case checkWarn:
			rep.Warnings++
		}
	}
	return rep
}

func (a *App) renderDoctor(rep doctorReport) error {
	if a.out.Format() != output.FormatTable {
		table := output.Table{
			Headers: []string{"CHECK", "STATUS", "DETAIL", "REMEDY"},
			Items:   rep,
		}
		for _, c := range rep.Checks {
			table.Rows = append(table.Rows, []string{c.Name, string(c.Status), c.Detail, c.Remedy})
		}
		if err := a.out.Render(table); err != nil {
			return err
		}
		return doctorError(rep)
	}

	width := 0
	for _, c := range rep.Checks {
		if len(c.Name) > width {
			width = len(c.Name)
		}
	}
	for _, c := range rep.Checks {
		a.out.Line("%s %-*s  %s", a.statusMark(c.Status), width, c.Name, c.Detail)
		if c.Remedy != "" {
			// Indented under the detail it belongs to, so that a report with
			// several remedies in it still reads as one line per check.
			a.out.Line("%*s  %s", width+2, "", c.Remedy)
		}
	}

	if rep.Problems == 0 {
		a.out.Line("")
		switch rep.Warnings {
		case 0:
			a.out.Line("No problems found.")
		default:
			a.out.Line("No problems found, %s above.", plural(rep.Warnings, "warning", "warnings"))
		}
	}
	return doctorError(rep)
}

// doctorError turns the report into an exit code.
//
// Warnings deliberately do not fail. A check that is permanently yellow on a
// healthy account would make the exit code useless for a health check, and an
// exit code nobody can rely on is one everybody ignores.
func doctorError(rep doctorReport) error {
	if rep.Problems == 0 {
		return nil
	}
	var names []string
	for _, c := range rep.Checks {
		if c.Status == checkFail {
			names = append(names, c.Name)
		}
	}
	// No operation name: the doctor did run, and "cannot check this
	// installation" would say the opposite of what happened.
	return cberr.New(cberr.KindOther, "", "",
		fmt.Sprintf("%s: %s", plural(rep.Problems, "problem", "problems"), strings.Join(names, ", ")))
}

func (a *App) statusMark(s checkStatus) string {
	mark, colour := "-", ""
	switch s {
	case checkOK:
		mark, colour = "✓", "\033[32m"
	case checkWarn:
		mark, colour = "⚠", "\033[33m"
	case checkFail:
		mark, colour = "✗", "\033[31m"
	}
	if !a.out.UsesColor() || colour == "" {
		return mark
	}
	return colour + mark + "\033[0m"
}

// ── small helpers ────────────────────────────────────────────────────────────

// configFilesRead lists the configuration files that exist and are therefore
// being read, in the order they are merged.
func configFilesRead(explicit string) []string {
	candidates := []string{SiteConfigPath, UserConfigPath()}
	if explicit != "" {
		candidates = []string{explicit}
	}
	var out []string
	for _, p := range candidates {
		if p != "" && isRegularFile(p) {
			out = append(out, p)
		}
	}
	return out
}

// certificateProblem reports whether the endpoint was unreachable because its
// certificate was not accepted. It is worth telling apart, because it is the one
// unreachable endpoint where the address and the network are both fine and
// looking at them is a waste of the afternoon.
func certificateProblem(err error) bool {
	var verify *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	return errors.As(err, &verify) || errors.As(err, &unknown) || errors.As(err, &hostname)
}

func isRegularFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func isDirectory(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// clockDetail says which way the drift goes, because that is what tells a user
// whether to look at this machine or at the server.
func clockDetail(skew time.Duration) string {
	d := skew.Abs().Round(time.Second)
	switch {
	case d == 0:
		return "in step with the server"
	case skew > 0:
		return d.String() + " ahead of the server"
	default:
		return d.String() + " behind the server"
	}
}

// plural renders a count with the right noun, which is the difference between
// "1 warning" and the "1 warnings" that makes a report look unfinished.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
