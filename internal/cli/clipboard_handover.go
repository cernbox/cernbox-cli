package cli

import (
	"context"
	"path"
	"strings"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
	"github.com/cernbox/cernbox-cli/pkg/client"
	"github.com/cernbox/cernbox-cli/pkg/clipboard"
)

// The clipboard between two people, rather than between two of your own
// machines: "cernbox copy --to marie" on one account, "cernbox paste --from
// einstein" on the other.
//
// Almost nothing new is needed for this, because the clipboard already keeps
// everything in one directory described by one manifest. A handover is that same
// directory, shared with the recipient:
//
//   - The slot is named after the recipient and is never the default one. The
//     whole slot directory becomes readable by them, so a handover left in the
//     slot the rest of your copies go to would hand over the next thing you
//     copied as well.
//   - Everything is staged into the slot, including sources already in CERNBox,
//     which an ordinary copy would merely point at. A pointer to a path of yours
//     is unreadable to somebody else, and staging it costs nothing: the server
//     copies it without the bytes passing through this process.
//   - The recipient reads the sender's paths directly. A share grants them that,
//     and it grants them nothing else: the clipboard's parent directories stay
//     unreadable, so only the one slot is exposed.

// handoverSlotFor works out which slot a copy for user goes in, and rejects the
// combinations that would share more than the caller meant to.
func handoverSlotFor(user string, slotGiven bool) (string, error) {
	if strings.TrimSpace(user) == "" {
		return "", cberr.Usagef("--to needs a username")
	}
	if slotGiven {
		return "", cberr.Usagef(
			"--to uses a slot of its own, named after the recipient, so it cannot be " +
				"combined with --slot. Everything in that slot is readable by them.")
	}
	slot := clipboard.HandoverSlot(user)
	if err := clipboard.ValidateSlot(slot); err != nil {
		return "", cberr.Usagef("%q is not a username this can hand over to: %v", user, err)
	}
	return slot, nil
}

// shareHandover grants the recipient one directory at the given role, and does
// nothing when they already have it.
//
// A stored handover grants viewer and nothing else: the recipient downloads, and
// the sender's own 'clipboard clear' is what ends it. A live one also grants
// editor on the pieces directory, which is the only thing the streaming protocol
// needs the far end to be able to write in.
func (a *App) shareHandover(ctx context.Context, slotDir, user, role string) error {
	info, err := a.client.Stat(ctx, slotDir)
	if err != nil {
		return err
	}
	if info.ID == "" {
		return cberr.New(cberr.KindOther, "share the clipboard slot", slotDir,
			"the server did not report a resource id for this path")
	}

	// A second copy to the same person reuses the share rather than adding one.
	// Sharing an already-shared path answers an error on some servers and adds a
	// duplicate on others, and neither is what the user asked for.
	perms, err := a.client.ListPermissions(ctx, info.ID)
	if err == nil {
		for _, p := range perms {
			if p.GrantedTo != nil && strings.EqualFold(p.GrantedTo.ID, user) {
				return nil
			}
		}
	}

	_, err = a.client.Share(ctx, info.ID,
		[]client.Recipient{{ID: user, Type: "user"}}, role, nil)
	if err != nil {
		return err
	}
	return nil
}

// unshareHandover takes back the grants a handover made, before the slot they
// were made on is removed.
//
// This has to happen first, and it has to happen at all. A share outlives the
// path it was made on: the directory goes to the trash and the share follows it
// there, where it shows up in "share list" for ever as a row naming a recycle-bin
// path. Twelve handovers made twelve of those before this existed.
//
// Failures are warnings rather than errors. The caller is on its way to deleting
// the slot, and refusing to do that because a share could not be tidied would
// leave the payload behind as well.
func (a *App) unshareHandover(ctx context.Context, root, slot string) {
	user, ok := clipboard.HandoverRecipient(slot)
	if !ok {
		return // an ordinary slot: nothing was ever shared
	}
	// Both directories. A live handover shares the pieces directory too, and its
	// grant is the one that outlives everything else if it is forgotten here:
	// nothing else ever looks at that path again.
	for _, dir := range []string{
		clipboard.StreamDir(root, slot),
		clipboard.SlotDir(root, slot),
	} {
		a.revokeAccess(ctx, dir, user)
	}
}

// revokeAccess removes one user's permissions on one directory, if it has any.
func (a *App) revokeAccess(ctx context.Context, dir, user string) {
	info, err := a.client.Stat(ctx, dir)
	if err != nil || info.ID == "" {
		return // already gone, or never there
	}
	perms, err := a.client.ListPermissions(ctx, info.ID)
	if err != nil {
		return
	}
	for _, p := range perms {
		if p.GrantedTo == nil || !strings.EqualFold(p.GrantedTo.ID, user) {
			continue
		}
		if err := a.client.RemovePermission(ctx, info.ID, p.ID); err != nil {
			a.out.Warn("could not take back %s's access to %s: %v", user, dir, err)
		}
	}
}

// senderClipboard is the clipboard root of somebody who has handed something
// over to the caller.
//
// The slot is found through the received shares rather than by building a path
// out of the sender's username. That is not caution for its own sake: a received
// share carries no path, so the only way to a shared slot is the resource id,
// and going through the share list also means the sender really did hand this
// over — a guessed path would be an attempt to read their home directory.
func (a *App) senderClipboard(ctx context.Context, sender, slot string) (string, error) {
	items, err := a.client.SharedWithMe(ctx)
	if err != nil {
		return "", err
	}

	var federated bool
	for _, it := range items {
		if it.SharedBy == nil || !strings.EqualFold(it.SharedBy.ID, sender) || it.Name != slot {
			continue
		}
		if it.Federated {
			// The paths in the manifest belong to the other provider's namespace,
			// so reading them as local paths would address the wrong files.
			federated = true
			continue
		}
		root, ok := client.SpacePathOfID(it.ID)
		if !ok {
			return "", cberr.New(cberr.KindOther, "paste", sender,
				"this server reports the shared slot in a form this client cannot turn into a path")
		}
		return path.Join(root, clipboard.Dir), nil
	}

	if federated {
		return "", cberr.New(cberr.KindOther, "paste", sender,
			"this handover came from another institution, which the clipboard does not cross yet")
	}
	return "", cberr.New(cberr.KindNotFound, "paste", sender,
		"nothing has been handed over to you by this person. They copy it with "+
			"'cernbox copy --to "+a.username(ctx)+" PATH'")
}

// username is the caller's own username, for the messages that have to name it.
// An error is reported as an empty name rather than failing a command that is
// only assembling a sentence.
func (a *App) username(ctx context.Context) string {
	me, err := a.client.Me(ctx)
	if err != nil || me == nil {
		return ""
	}
	return me.Username
}

// incomingHandovers are the slots other people have shared with the caller,
// each with the manifest read from the sender's space.
//
// A sender that has shared a slot but written no manifest into it yet, or whose
// slot has since been cleared, is skipped rather than reported: the share
// outlives the copy, and a listing is not the place to explain that.
func (a *App) incomingHandovers(ctx context.Context) []slotView {
	me := a.username(ctx)
	if me == "" {
		return nil
	}
	slot := clipboard.HandoverSlot(me)

	items, err := a.client.SharedWithMe(ctx)
	if err != nil {
		return nil
	}

	var out []slotView
	for _, it := range items {
		if it.Name != slot || it.SharedBy == nil || it.Federated {
			continue
		}
		root, ok := client.SpacePathOfID(it.ID)
		if !ok {
			continue
		}
		m, err := a.readSlot(ctx, path.Join(root, clipboard.Dir), slot)
		if err != nil {
			continue
		}
		out = append(out, slotView{Manifest: m, From: it.SharedBy.ID})
	}
	return out
}
