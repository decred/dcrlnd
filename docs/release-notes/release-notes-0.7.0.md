# dcrlnd v0.7.0

This release is the first release for the v2.0 line of Decred tool releases.

While dcrlnd itself has few changes, it brings in all changes inherited from
the [corresponding dcrwallet 
release](https://github.com/decred/dcrwallet/releases/tag/release-v2.0.0).

The following is the list of dcrlnd-specific changes for v0.7.0.

- Additional logging for [pathfinding](https://github.com/decred/dcrlnd/pull/212) 
  and [topology changes](https://github.com/decred/dcrlnd/pull/209) to help
  debug payment failures.

- Fixed issue with [future timestamps](https://github.com/decred/dcrlnd/pull/211) 
  causing the gossiper to fail to fetch updates upon reconnecting to a peer.

- [Improved the performance](https://github.com/decred/dcrlnd/pull/208) of the
  Mission Control Store, in particular for clients that make only occasional
  payments or otherwise sit idle for long periods of time.

- [Exposed the 
  `disablerelaytx`](https://github.com/decred/dcrlnd/pull/213/commits/36a71209b25c5fa67ab1160561a31e30bf75145f)
  config argument that was recently introduced by dcrwallet to disable receiving
  mempool transactions from peers.

- [Passing the aezeed
  birthday](https://github.com/decred/dcrlnd/pull/213/commits/53c91821abbe4d9a93c61b23a853960275e29a6d)
  to dcrwallet when creating or restoring a dcrlnd instance, making both
  operations significantly faster, in particular creation of wallets from new
  seeds, which remove the need for long-running and cpu-intensive account and
  address discovery stages.



# Contributors (Alphabetical Order)

  - David Hill
  - Matheus Degiovani

