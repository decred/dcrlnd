# dcrlnd v0.3.0

_Please note that while Bitcoin's Lightning Network has been in production for a few years, Decred's version still hasn't seen extensive use in mainnet. Users should be mindful of the total amount of funds comitted to dcrlnd wallets and channels._

This is a major dcrlnd release including significant amount of changes.

This release brings dcrlnd in line with the upstream lnd [release v0.11.1](https://github.com/lightningnetwork/lnd/releases/tag/v0.11.1-beta) and also includes ports for versions [v0.11.0](https://github.com/lightningnetwork/lnd/releases/tag/v0.11.0-beta), [v0.10.0](https://github.com/lightningnetwork/lnd/releases/tag/v0.10.0-beta) and [v0.9.0](https://github.com/lightningnetwork/lnd/releases/tag/v0.9.0-beta).

## Database Migrations

This release contains database migrations for the new TLV encoding of invoices, payment address indexing and close summary information. Old versions of dcrlnd cannot use the new database version once these migrations are applied.

## Changelog

The major Decred-specific feature introduced in this release is the ability to run a dcrlnd instance connected to a dcrwallet running in SPV mode. This is useful mostly for Decrediton users that will now have the option to run dcrlnd even when their wallet is using the SPV configuration.

### Node Syncing Config

CLI users now have two options for the `--node` argument:

  - `--node=dcrd` instructs dcrlnd to connect to a dcrd instance for on-chain operations.
  - `--node=dcrw` instructs dcrlnd to use the underlying dcrwallet instance for on-chain operations.

When using `--node=dcrd`, the `--dcrd.`-namespaced options should be used to configure the connection to the underlying dcrd node.

When using `--node=dcrw`, either the `--dcrd.`-namespaced options should be used, in order to use an _embedded_ dcrwallet instance (that is, dcrwallet runs automatically inside dcrlnd) **or** the `--dcrw.`-namespaced options should be used to configure a __remote__ dcrwallet instance.

Note that SPV mode is only supported on remote dcrwallet instances.

For hub nodes (that is, nodes that are online most of the time and offer the ability to receive open channel requests) the recommended config setting is to use embedded wallets with a dcrd instance.

### Wumbo Channel Support

This release adapts the [Wumbo](https://github.com/lightningnetwork/lnd/pull/4429) feature for the realities of Decred. Wumbo channel support can be enabled by running dcrlnd with `--protocol.wumbo-channels` and has a global maximum channel size of 500 DCR.

### Relevant Upstream Changes

The following is a non-exhaustive list of the relevant upstream changes that were ported to dcrlnd. These include changes from the upstream [v0.9](https://github.com/decred/dcrlnd/pull/74), [v0.10](https://github.com/decred/dcrlnd/pull/99) and [v0.11](https://github.com/decred/dcrlnd/pull/103) lines. Please refer to the respective upstream releases for additional information.

  - Multi Path Payment (MPP) support so that a single payment can be split among multiple channels.
  - Track payments with a new Payment Address field.
  - Additional TLV data sent in payments, which allows creating new use cases to deliver payload data via LN payments.
  - Keysend payment experiment which allows spontaneous payments without the need for a precreated invoice.
  - Upfront shutdown script support to enforce channel closure to pay to pre-configured addresses.
  - HTLC Interception API to allow creation of custom payment forwarding engines.
  - Additional data in Channel Close Summaries.
  - Add ability to limit max remote pending HTLC amount during channel opening.
  - Anchor outputs experimental feature.
  - External channel funding experimental feature.
  - Healthchecks to ensure adequate operating conditions of the node
  - Several bug fixes throughout the app.

# Porting Effort

A total of 450 upstream PRs were considered for inclusion. The list of of PRs can be found in the acompanying [upstream-prs.csv](/docs/upstream-prs.csv) doc.


# Decred Contributors (Alphabetical Order)

  - Fernando Guisso
  - Matheus Degiovani
  - Ole Andre Birkedal

# Acknowledgement

The majority of the work included in this release is from features and bugfixes performed by the contributors to the upstream [lnd](https://github.com/lightningnetwork/lnd) project that were ported to Decred.

We wish to sincerely thank them for providing such a high quality project and hope we can continue to contribute in building a large scale and cross-coin LN ecosystem.

