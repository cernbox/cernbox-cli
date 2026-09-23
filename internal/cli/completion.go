package cli

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/pathspec"
	"github.com/spf13/cobra"
)

// completionTimeout bounds everything a single press of TAB may do. The user is
// sitting in front of a cursor that does not come back until this returns, so a
// slow or unreachable server has to become "no suggestions" quickly rather than
// a hang the shell cannot explain.
const completionTimeout = 3 * time.Second

// completeFunc is the signature cobra calls to complete an argument.
type completeFunc = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective)

// isCompletionRequest reports whether the command being run is cobra's hidden
// completion helper rather than something the user typed.
func isCompletionRequest(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() == cobra.ShellCompRequestCmd {
			return true
		}
	}
	return false
}

// completionContext returns the context a completion runs under.
func completionContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), completionTimeout)
}

// ready prepares the client for a completion, and reports whether there is one
// to use.
//
// Everything about this is best effort. A shell asks for completions on a
// machine with no configuration, no credentials and no network just as readily
// as on a working one, and in every one of those cases the right answer is no
// suggestions rather than an error: the user is staring at a half-typed command
// line, and anything printed there lands in the middle of it.
func (a *App) ready() bool {
	if a.client != nil {
		return true
	}
	if !a.completing || a.connectTried {
		return false
	}
	a.connectTried = true // one attempt per process, however many arguments ask
	if err := a.connect(); err != nil {
		return false
	}
	return a.client != nil
}

// ── paths ────────────────────────────────────────────────────────────────────

// completeRemote completes an argument that is always a CERNBox path.
func (a *App) completeRemote(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return a.remoteCandidates(toComplete)
}

// completeTransfer completes an argument that may name either side of a
// transfer. Only a word already marked as remote is completed from the server;
// anything else is left to the shell, which knows the local filesystem better
// than this process does.
func (a *App) completeTransfer(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if !looksRemote(toComplete) {
		return nil, cobra.ShellCompDirectiveDefault
	}
	return a.remoteCandidates(toComplete)
}

// looksRemote reports whether a partially typed word has already been marked as
// a CERNBox path, by the cb: prefix or by a space alias.
func looksRemote(arg string) bool {
	if strings.HasPrefix(arg, pathspec.RemotePrefix) {
		return true
	}
	spec, err := pathspec.ParseTransfer(arg)
	return err == nil && spec.IsRemote()
}

// remoteCandidates lists the children of the directory the user is part way
// through typing.
func (a *App) remoteCandidates(toComplete string) ([]string, cobra.ShellCompDirective) {
	if !a.ready() {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext()
	defer cancel()

	dir, frag := pathspec.SplitForCompletion(toComplete)
	base, err := a.resolve(ctx, dirArgument(dir))
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	entries, err := a.client.List(ctx, base)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	// A directory is completed without a trailing space so that the next
	// keystroke carries on into it, the way completing a local directory does.
	directive := cobra.ShellCompDirectiveNoFileComp
	var out []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name, frag) {
			continue
		}
		// Hidden entries only appear once the user has shown interest in them,
		// which is the rule every shell uses and which keeps the CLI's own
		// .cernbox directory out of the way.
		if strings.HasPrefix(e.Name, ".") && !strings.HasPrefix(frag, ".") {
			continue
		}
		if e.IsDir {
			out = append(out, dir+e.Name+"/")
			directive |= cobra.ShellCompDirectiveNoSpace
			continue
		}
		out = append(out, dir+e.Name)
	}
	sort.Strings(out)
	return out, directive
}

// dirArgument turns the directory half of a partially typed path into something
// ParseRemote accepts. "cb:" and "" name no path, and both mean the home space,
// which is where a bare relative path is resolved.
func dirArgument(dir string) string {
	if dir == "" || dir == pathspec.RemotePrefix {
		return "."
	}
	return dir
}

// ── identifiers ──────────────────────────────────────────────────────────────

// completeSpace completes a space alias.
func (a *App) completeSpace(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if !a.ready() {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext()
	defer cancel()

	spaces, err := a.client.Spaces(ctx)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, s := range spaces {
		if alias := client.SpaceAlias(s); strings.HasPrefix(alias, toComplete) {
			out = append(out, alias+"\t"+s.Name)
		}
	}
	sort.Strings(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeSlot completes a clipboard slot name.
func (a *App) completeSlot(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if !a.ready() {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext()
	defer cancel()

	root, err := a.clipboardRoot(ctx)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	entries, err := a.client.List(ctx, root)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, e := range entries {
		if e.IsDir && strings.HasPrefix(e.Name, toComplete) {
			out = append(out, e.Name)
		}
	}
	sort.Strings(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeVersion completes the version key of the path in the first argument.
func (a *App) completeVersion(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 || !a.ready() {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext()
	defer cancel()

	// Versions are addressed by resource id, not by path, so the path has to be
	// resolved to one first.
	info, err := a.statResolved(ctx, args[0])
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	versions, err := a.client.ListVersions(ctx, info.ID)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, v := range versions {
		if strings.HasPrefix(v.Key, toComplete) {
			out = append(out, v.Key+"\t"+v.Modified.Format(time.RFC3339))
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeShareID completes the id of a share on the path in the first
// argument, or of a public link when links is set. These ids are made for
// machines, and completing them is the difference between a command somebody
// can type and one they have to copy and paste into.
func (a *App) completeShareID(links bool) completeFunc {
	return func(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 || !a.ready() {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		ctx, cancel := completionContext()
		defer cancel()

		info, err := a.statResolved(ctx, args[0])
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		perms, err := a.client.ListPermissions(ctx, info.ID)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var out []string
		for _, perm := range perms {
			if (perm.Link != nil) != links || !strings.HasPrefix(perm.ID, toComplete) {
				continue
			}
			out = append(out, perm.ID+"\t"+shareLabel(perm))
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// shareLabel describes a share well enough to tell two of them apart.
func shareLabel(p client.Permission) string {
	switch {
	case p.Link != nil:
		return strings.TrimSpace(p.Link.Type + " link")
	case p.GrantedTo != nil && p.GrantedTo.DisplayName != "":
		return p.GrantedTo.DisplayName
	case p.GrantedTo != nil:
		return p.GrantedTo.ID
	default:
		return p.Role
	}
}

// completeToken completes the id of an app token on the account.
func (a *App) completeToken(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if !a.ready() {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext()
	defer cancel()

	clients, err := a.client.ListConnectedClients(ctx)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, c := range clients {
		if !strings.HasPrefix(c.ID, toComplete) {
			continue
		}
		out = append(out, strings.TrimSuffix(c.ID+"\t"+c.Name, "\t"))
	}
	sort.Strings(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeApp completes the name of an application the server can open files
// with.
func (a *App) completeApp(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if !a.ready() {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext()
	defer cancel()

	types, err := a.client.ListApps(ctx)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range types {
		for _, name := range t.Apps {
			if seen[name] || !strings.HasPrefix(name, toComplete) {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

// ── plumbing ─────────────────────────────────────────────────────────────────

// byPosition routes completion by argument position: the first function
// completes the first argument, the second the second, and so on. Arguments
// past the last function get nothing, which is how a command that takes two
// paths stops suggesting a third.
func byPosition(fns ...completeFunc) completeFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) >= len(fns) {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return fns[len(args)](cmd, args, toComplete)
	}
}

// fixedCompletions completes from a list known at build time, for a flag whose
// values are an enumeration.
func fixedCompletions(values ...string) completeFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		var out []string
		for _, v := range values {
			if strings.HasPrefix(v, toComplete) {
				out = append(out, v)
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeLocal leaves the argument to the shell, which knows the local
// filesystem better than this process does.
func completeLocal(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveDefault
}

// completeFlagValues registers a fixed set of values for a flag, and says so
// loudly when the flag does not exist: a completion attached to nothing is a
// bug that otherwise shows up only as a shell that quietly does not complete.
func completeFlagValues(cmd *cobra.Command, flag string, values ...string) {
	if err := cmd.RegisterFlagCompletionFunc(flag, fixedCompletions(values...)); err != nil {
		panic("completion for unknown flag --" + flag + " on " + cmd.Name() + ": " + err.Error())
	}
}

// registerGlobalCompletions attaches completions to the flags every command
// accepts.
func registerGlobalCompletions(cmd *cobra.Command) {
	completeFlagValues(cmd, "output", "table", "json", "csv")
	completeFlagValues(cmd, "method", "kerberos", "device", "app-token", "basic", "token")
}

// ── the map ──────────────────────────────────────────────────────────────────

// argCompletions says how every runnable command completes its arguments.
//
// It is one table rather than a field on each command so that the whole
// behaviour can be read at once, and so that a command added without a decision
// about completion is caught by a test instead of silently falling back to
// completing local filenames — which, for a CLI whose arguments are mostly
// remote paths, is always wrong.
func (a *App) argCompletions() map[string]completeFunc {
	remote := a.completeRemote     // a CERNBox path
	transfer := a.completeTransfer // either side, completed only when marked cb:
	local := completeLocal         // a path on this computer
	none := cobra.NoFileCompletions

	return map[string]completeFunc{
		"login":  none,
		"logout": none,
		"status": none,
		"whoami": none,

		"ls":   remote,
		"stat": byPosition(remote),
		"find": byPosition(remote),
		"du":   remote,
		"cat":  remote,

		"mkdir": remote,
		"touch": remote,
		"rm":    remote,
		"mv":    byPosition(remote, remote),

		"cp":      byPosition(transfer, transfer),
		"get":     byPosition(remote, local),
		"put":     byPosition(local, remote),
		"sync":    byPosition(transfer, transfer),
		"archive": remote,

		"copy":            transfer,
		"paste":           byPosition(transfer),
		"clipboard list":  none,
		"clipboard clear": a.completeSlot,

		"share create":   byPosition(remote),
		"share list":     byPosition(remote),
		"share update":   byPosition(remote, a.completeShareID(false)),
		"share remove":   byPosition(remote, a.completeShareID(false)),
		"share received": none,

		"link create":   byPosition(remote),
		"link list":     byPosition(remote),
		"link update":   byPosition(remote, a.completeShareID(true)),
		"link remove":   byPosition(remote, a.completeShareID(true)),
		"link password": byPosition(remote, a.completeShareID(true)),

		"ocm invite create": none,
		"ocm invite list":   none,
		"ocm invite accept": none, // an invitation token is pasted in, never typed
		"ocm contacts":      none,
		"ocm providers":     none,
		"ocm received":      none,

		// Trash keys are not completed on purpose. The only way to learn them is
		// to list the whole trash bin, which on a real account is enormous and
		// slow — far too much work to do behind a key press.
		"trash list":    none,
		"trash restore": none,
		"trash purge":   none,

		"versions list":     byPosition(remote),
		"versions restore":  byPosition(remote, a.completeVersion),
		"versions download": byPosition(remote, a.completeVersion),

		"space list": none,
		"space info": byPosition(a.completeSpace),

		"open": byPosition(remote),
		"apps": none,

		"token list":   none,
		"token revoke": byPosition(a.completeToken),
		"token create": none,

		"version": none,
	}
}

// flagCompletions lists the flags whose values are an enumeration, so that they
// can be completed rather than remembered.
var flagCompletions = map[string]map[string][]string{
	"ls":           {"sort": {"name", "time", "size"}},
	"archive":      {"format": {"tar", "zip"}},
	"share create": {"role": {"viewer", "editor", "collab", "denied"}},
	"share update": {"role": {"viewer", "editor", "collab", "denied"}},
	"link create":  {"role": {"viewer", "editor"}},
	"link update":  {"role": {"viewer", "editor"}},
	"space list":   {"type": {"personal", "project"}},
	"token create": {"permission": {"read", "write"}},
	"open":         {"view-mode": {"read", "write"}},
}

// registerCompletions attaches the tables above to the command tree.
func registerCompletions(root *cobra.Command, app *App) {
	args := app.argCompletions()
	walkCommands(root, nil, func(path string, cmd *cobra.Command) {
		if fn, ok := args[path]; ok {
			cmd.ValidArgsFunction = fn
		}
		for flag, values := range flagCompletions[path] {
			completeFlagValues(cmd, flag, values...)
		}
	})
	app.registerDynamicFlagCompletions(root)
}

// registerDynamicFlagCompletions covers the flags whose values have to be
// looked up on the server.
func (a *App) registerDynamicFlagCompletions(root *cobra.Command) {
	if open, _, err := root.Find([]string{"open"}); err == nil && open.Name() == "open" {
		if err := open.RegisterFlagCompletionFunc("app", a.completeApp); err != nil {
			panic("completion for --app on open: " + err.Error())
		}
	}
}

// walkCommands visits every runnable command in the tree, with the path a user
// would type to reach it.
func walkCommands(cmd *cobra.Command, prefix []string, visit func(path string, cmd *cobra.Command)) {
	for _, child := range cmd.Commands() {
		if child.Name() == cobra.ShellCompRequestCmd || child.Name() == "help" {
			continue
		}
		path := append(append([]string{}, prefix...), child.Name())
		if child.Runnable() {
			visit(strings.Join(path, " "), child)
		}
		walkCommands(child, path, visit)
	}
}
