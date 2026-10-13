// Package workspace is prt's multi-repo mode (M5.3): `prt --root DIR`, or
// prt started outside a git repository, serves every repository under a
// directory in one picker.
//
// It does two things:
//
//   - Discover walks the filesystem under a root (no git process) and lists
//     the repositories it finds, each with a workspace key: its path relative
//     to the root (model.CleanRepoKey), or the root's base name when the root
//     itself is a repository.
//   - Client multiplexes one daemon.Client per repository (normally an
//     in-process client with its own store, log and config under its own
//     .git/prt) into a single daemon.Client that the TUI and the workspace
//     daemon use exactly like a single-repo client.
//
// # Item ids
//
// Per-repo stores keep their repo-local ids ("pr:12", "branch:feat/x"), so
// stores written before M5.3 open unchanged. Every id leaving the Client is
// rewritten to model.RepoItemID(key, id) = "<key>::<id>" (and Item.Repo is
// set to the key); every id entering it is split with model.SplitRepoItemID
// and routed to that repo's client with the local id. An id without a repo
// prefix, or with an unknown key, is an error.
//
// # Qualified references
//
// Search and add accept a repo qualifier (ParseQualified):
//
//	<repo>#<digits>   a PR of one repo        acme/billing-svc#12, billing-svc#12
//	<repo>:<ref>      a ref of one repo       billing-svc:feat/x, team/portal:main
//	!<qualified>      closed-PR search        !billing-svc:fix  → ("billing-svc", "!fix")
//
// The repo part names a repo by key, by label (owner/name, any case), by a
// unique last path element of a key, or by a unique repo name of an
// owner/name label (Client.Resolve). A qualifier that does not resolve is
// not an error: the whole query is then treated as unqualified, so a branch
// literally named "feat#2" still works.
package workspace
