# dcrlnd v0.5.1


## Database Migrations

This release contains a [database
migration](https://github.com/decred/dcrlnd/pull/203) that fixes an issue
introduced by a prior migration (migration 20) ported from upstream lnd. This
issue could cause channels that were opened before v0.5.0 of the software to not
be properly closed (or detected as closed), causing affected channels to still
be listed as opened.

## Various New Features

Added the [rescan wallet](https://github.com/decred/dcrlnd/pull/198) 

## Dependencies Updates

Updated [dcrwallet](https://github.com/decred/dcrlnd/pull/202) to the latest
version of the v1.8 release line.
