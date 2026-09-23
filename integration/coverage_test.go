//go:build integration

package integration_test

import (
	"sort"
	"strings"
	"testing"
)

// coveredCommands maps every command in the tree to the test that exercises it
// against the dev environment.
//
// This is what makes "everything is tested" checkable rather than asserted.
// TestEveryCommandIsCovered fails when the binary grows a command that is not
// listed here, so adding one forces a decision about how it gets tested — which
// is the moment to make it, not six months later.
var coveredCommands = map[string]string{
	// Authentication.
	"login":  "TestLoginAndLogout, TestLoginReportsTheProvider",
	"logout": "TestLoginAndLogout",
	"status": "TestStatusReportsServerAndCredential, TestStatusJSON",
	"whoami": "TestWhoami",

	// Browsing and metadata.
	"stat": "TestMoveAndCat, TestStatOnMissingPathExitsFive",
	"ls":   "TestLsLongAndRecursive, TestLsCSV, TestLsStreamingJSON",
	"find": "TestFindFallsBackToWalking",
	"du":   "TestDu",
	"cat":  "TestMoveAndCat",

	// Namespace.
	"mkdir": "TestMkdirListAndRemove, TestMkdirParents",
	"touch": "TestTouchCreatesAnEmptyFile",
	"rm":    "TestMkdirListAndRemove",
	"mv":    "TestMoveAndCat",

	// Transfers.
	"cp":   "TestServerSideCopy, TestCpRefusesAmbiguousPaths",
	"get":  "TestPutAndGetRoundTrip, TestRecursiveUploadAndDownload",
	"put":  "TestPutAndGetRoundTrip, TestLargeFileUsesResumableUpload",
	"sync": "TestSyncPushAndPull, TestSyncDelete",

	// The cross-machine clipboard.
	"copy":            "TestClipboardRoundTrip, TestClipboardReferencesARemotePathWithoutUploading, TestClipboardCarriesADirectory",
	"paste":           "TestClipboardRoundTrip, TestClipboardPasteInsideCERNBoxMovesNoData, TestClipboardRefusesToOverwriteWithoutForce",
	"clipboard list":  "TestClipboardListShowsTheSlot",
	"clipboard clear": "TestClipboardClearReleasesStagedBytes, TestClipboardClearLeavesTheOriginalAlone",

	// Sharing.
	"share create":   "TestShareLifecycle, TestShareIsVisibleToTheRecipient, TestShareWithGroup",
	"share list":     "TestShareLifecycle, TestShareListWithoutPathShowsEverythingShared",
	"share update":   "TestShareLifecycle",
	"share remove":   "TestShareLifecycle",
	"share received": "TestShareIsVisibleToTheRecipient",

	// Public links.
	"link create":   "TestPublicLinkLifecycle, TestLinkWithExpiryAndName",
	"link list":     "TestPublicLinkLifecycle",
	"link remove":   "TestPublicLinkLifecycle",
	"link password": "TestLinkPasswordNeedsATerminal",

	// Federated sharing.
	"ocm invite create": "TestOCMInviteCreateAndList",
	"ocm invite list":   "TestOCMInviteCreateAndList",
	"ocm invite accept": "TestOCMInviteAcceptEstablishesAContact",
	"ocm contacts":      "TestOCMInviteAcceptEstablishesAContact, TestOCMContactRemoval",
	"ocm providers":     "TestOCMProvidersListsThePartner",
	"ocm received":      "TestOCMShareReachesThePartner",

	// History.
	"trash list":        "TestTrashRoundTrip",
	"trash restore":     "TestTrashRoundTrip",
	"trash purge":       "TestTrashPurge, TestTrashPurgeAllRefusesNonInteractively",
	"versions list":     "TestVersionsRoundTrip, TestVersionsListRejectsDirectory",
	"versions restore":  "TestVersionsRoundTrip",
	"versions download": "TestVersionsRoundTrip",

	// Spaces.
	"space list": "TestSpaceListIncludesHome, TestSpaceListFilteredByType",
	"space info": "TestSpaceInfo",

	// Applications.
	"open": "TestOpenWebLink, TestOpenReturnsAnApplicationLink, TestOpenWithExplicitApp",
	"apps": "TestAppsListsMimeTypes",

	// App tokens.
	"token list":   "TestTokenList",
	"token revoke": "TestTokenRevokeUnknownIDIsIdempotent",
	"token create": "TestTokenCreateExplainsWhereToGo",

	// Miscellaneous.
	"version": "TestVersionWorksWithoutCredentials",
}

// TestEveryCommandIsCovered checks the map above against the binary itself.
func TestEveryCommandIsCovered(t *testing.T) {
	e := setup(t)

	out := e.mustRun("__commands")
	var missing []string
	present := map[string]bool{}

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		cmd := strings.TrimSpace(line)
		if cmd == "" {
			continue
		}
		present[cmd] = true
		if _, ok := coveredCommands[cmd]; !ok {
			missing = append(missing, cmd)
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("these commands have no integration coverage:\n  %s\n\n"+
			"Add a test that exercises each against the dev environment, then list it in "+
			"coveredCommands. If a command genuinely cannot be tested here, say why in the map.",
			strings.Join(missing, "\n  "))
	}

	// The other direction: an entry for a command that no longer exists is a
	// stale claim, and stale claims are how a coverage map stops meaning
	// anything.
	var stale []string
	for cmd := range coveredCommands {
		if !present[cmd] {
			stale = append(stale, cmd)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("coveredCommands lists commands that no longer exist:\n  %s",
			strings.Join(stale, "\n  "))
	}
}
