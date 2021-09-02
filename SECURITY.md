# Security Policy

## Reporting a Vulnerability

The Decred project runs a bug bounty program which is approved by the stakeholders and is funded by the Decred treasury.

Please refer to the bounty website to understand the [scope](https://bounty.decred.org/#Scope) and how to [submit](https://bounty.decred.org/#Submit%20Vulnerability) a vulnerability.

https://bounty.decred.org/

## Supported Versions

`dcrlnd` is part of Decred's [Bug Bounty Program](https://bounty.decred.org)
on an experimental basis while we haven't yet deployed into mainnet.

Additionally, given the current nature of this work as a fork from the original
`lnd` code, bugs that have been submitted to the upstream `lnd` project are **not**
eligible for the bug bounty program _unless_ the following points apply:

  - The bug affects a mainnet worthy release of `dcrlnd`;
  - The fix for the bug was _not_ merged from the upstream repo while a
  substantial amount of upstream commits that are newer than the relevant one
  were merged;
  - The bug is not critical to `lnd` but it is to `dcrlnd`.