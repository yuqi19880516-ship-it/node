# Two-step sampling of the anchor audit: parameters, windows, upgrade, residuals

Operator and governance notes for the two-step sampling flow of the anchor
audit (Profiles whose anchor evidence requirement carries
`commitment_schema_version = 2`). The message, fields and hash preimages are
defined by the pinned wire release; this page describes how the node realises
them and what the chain owners have to set.

## Switching it on

Nothing is on after the upgrade. The flow starts when all three hold:

1. `beacon.vrf_required_from_height` (Hub) is set and reached.
2. `verification.anchor_list_window_blocks` and
   `verification.anchor_position_beacon_blocks` (Task) are both set
   (recommended 1,800 and 8; see the guidance for `m` below).
3. A Profile version with anchor commitment schema version 2 is registered.
   The Hub refuses the registration unless 1 and 2 hold.

Once a two-step Profile version is listed, `MsgUpdateHubParams` refuses to set
`beacon.vrf_required_from_height` to zero or to a height above the current one.
To drop the requirement, delist those Profile versions first. If the
requirement is nevertheless absent (a genesis that carries such a Profile on a
chain without it), every selected audit ends at its first draw visit as
`NO_VERDICT`, source `TIMEOUT`, with the reason

```
the chain did not draw positions for the recorded commitment list: the proposer-VRF beacon is not required at this height (beacon.vrf_required_from_height), so two-step sampling cannot draw
```

The Worker is paid and nothing is slashed. No task of that Profile is audited
while this lasts.

## Heights of one audit

`S` is the block that records the selection (normally the sampling height).
`L`, `m` are the list window and the beacon count the task's frozen parameters
carry, or 1,800 and 8 when they carry none. `G` = 256 is
`AnchorPositionDrawGraceBlocks`, a protocol constant. `O`, `P`, `V` are the
opening, proof and verdict windows.

| Field | Value |
|---|---|
| `list_deadline_height` | `S + L`. The block of the deadline is in time. |
| `verdict_deadline_height` | `S + L + m + G + O + P + V`. Written at selection, never moved. |
| `commitment_list_height` | `H_L`, the block of the first accepted list message. |
| `position_beacon_first_height`, `position_beacon_last_height` | `H_L + 1`, `H_L + m`. Replaced by `h + 1`, `h + m` when a window is restarted at height `h`. |
| `positions_height` | `D`, the block whose EndBlock drew: `position_beacon_last_height` or later. |
| `opening_deadline_height` | `D + O` |
| `proof_deadline_height` | `D + O + P` |

- The chain draws at any `D <= S + L + m + G`, that is as long as
  `D + O + P + V <= verdict_deadline_height`. A draw that would leave less than
  the three full windows is not made: the audit ends `NO_VERDICT` (paid).
- `G` is the chain's own slack for a late EndBlock visit or a restarted beacon
  window. A list recorded in the last block of the list window has all of it.
  It shortens no window of the Worker.
- **The longest life of an audit is `L + m + G + O + P + V` blocks from `S`.**
  `cleanup.task_terminal_summary_retention_blocks` must be larger (parameter
  validation enforces it, with the 1,800 + 8 fallback counted when the body
  sets no two-step windows), and a Worker must keep the material of a selected
  task for at least that long. With the recommended values at 500 ms blocks:
  1,800 + 8 + 256 + 1,800 + 20,000 + 7,200 = 31,064 blocks.
- All deadlines are block heights. A halted or slow chain consumes none.

## The list message

`MsgSubmitAnchorCommitmentList` (CLI `submit-anchor-commitment-list`), signed
by the winner Worker's current service:

| Situation | Answer |
|---|---|
| First message by `list_deadline_height`, size `32 x` rows, location under `da_uri_prefix` | `APPLIED`; hash, size, location and `H_L` recorded |
| Same hash and size, same location, any later height | `NOOP` |
| Same hash and size, another location, audit not final | `APPLIED`; only `commitment_list_uri` changes. Before or after the draw, before or after the list deadline. The location must lie under `da_uri_prefix` |
| Same hash and size, another location, verdict final | `NOOP`; the location stays |
| Another hash or size after a list is recorded | refused: the first recorded list is final |
| No list recorded and the list deadline has passed | refused |

## Outcomes

| Situation | Verdict / source | Consequence |
|---|---|---|
| No list by the list deadline, PASS round | REJECT / TIMEOUT, recorded one block after the deadline | timeout fault, as a missing opening |
| No list by the list deadline, round not passed | REJECT / TIMEOUT recorded the same way | applied only by a settlement at or after the verdict deadline, as a missing opening is |
| List recorded, chain could not draw in time | NO_VERDICT / TIMEOUT | paid, nothing slashed |
| VRF beacon not required | NO_VERDICT / TIMEOUT, reason above | paid, nothing slashed |
| After the draw | the one-step outcomes | unchanged |

## Upgrade

- Plan name `v0.6.4-two-step-sampling`. Task module consensus version 4 -> 5.
- The Task parameter commitment frames every field, so the fields added since
  the anchor audit release change the hash of an unchanged body. The migration
  writes the live body as a new params version with its commitment and archives
  the replaced version; rows bound to it keep reading it.
- If `cleanup.task_terminal_summary_retention_blocks` is not above the audit
  life given above, the migration raises it (to the default 60,480, or to the
  audit life plus one if that is larger) and logs the repair. Run
  `types.RepairAnchorParams` on the live parameters before the upgrade height to
  see whether this applies, and confirm the value by governance afterwards.
- Audits in flight keep the deadlines they carry.
- A genesis exported by the previous binary carries the previous commitment and
  is refused by a cold start of this binary; export after the upgrade instead.

## Residual of the position beacon: re-roll by the last contributing proposers

Status: not closed, by decision (no protocol change in this round). This section states what remains and what the chain owners can set. Nothing in it affects an honest Worker: a re-roll never produces a fault, it only changes which rows are drawn.

### What the draw depends on

- The list message is included in block `H_L`. The Worker chooses `H_L` freely inside the list window (`L` blocks, recommended 1,800).
- The positions are a hash of the first beacon, the list hash and the aggregate of the `m` proposer-VRF beacons of heights `H_L + 1 .. H_L + m`.
- The VRF input of a height is the chain id, the height, the previous beacon and the proposer of the block. A proposer cannot choose its value. It can decline to propose in its round-0 slot; the block of that height is then made by the proposer of the next round, with a different value, and every later beacon changes with it.

### Who can do what

Let the coalition be a set of validators that colludes with the Worker, with share `a` of the voting power, and let `j` be the number of consecutive round-0 proposer slots it holds at the end of the contributing heights (`H_L + m - j + 1 .. H_L + m`), `0 <= j <= m`.

1. **Re-roll (any `m`).** Before each of its `j` trailing slots the coalition knows every earlier beacon and can compute its own remaining values, so it sees the complete draw that publishing would give. If that draw hits a forged row it withholds the block, an outside proposer fills the height, and the coalition gets a new complete draw to look at before its next slot. With `j` trailing slots it sees up to `j + 1` candidate draws and keeps the first one that misses.
   - If one draw detects the forgery with probability `p`, the audit detects it with probability `p^(j+1)`. One colluding validator in the last slot (`j = 1`) turns `p` into `p^2`.
   - `p = 1 - C(R - f, 2) / C(R, 2)` for `f` forged rows of `R` and the 2 positions drawn per audit.
   - Each withheld slot costs the validator that block's proposer income and is visible on chain: the block of that height is committed in a round above 0.
2. **Placement by the Worker.** Because the Worker picks `H_L`, it picks who holds the last slots. With the deterministic proposer rotation of a stable validator set, a height whose trailing slot belongs to the coalition exists in the window whenever the coalition proposes at least once every `L + 1` blocks, that is for every share `a` of about `1 / L` or more (0.06 % at `L` = 1,800): the placement succeeds with probability 1. Under the idealisation of independent proposers the probability is `1 - (1 - a)^(L+1)` for `j = 1` and `1 - (1 - a^j)^(L+1)` for a run of `j`:

   | share `a` | `j = 1` | `j = 2` | `j = 3` | `j = 4` |
   |---|---|---|---|---|
   | 0.1 % | 83.5 % | 0.2 % | 0 | 0 |
   | 1 % | 100 % | 16.5 % | 0.2 % | 0 |
   | 5 % | 100 % | 98.9 % | 20.2 % | 1.1 % |
   | 10 % | 100 % | 100 % | 83.5 % | 16.5 % |
   | 20 % | 100 % | 100 % | 100 % | 94.4 % |

   In the real rotation `j` is bounded by the longest run `J` of consecutive round-0 slots the coalition holds, which depends on the validator set and on the order of the rotation, not on the table above. If the tx misses the intended block the Worker loses nothing: it gets an ordinary draw.
3. **Choosing the list after seeing the draw (only if `J >= m + 1`).** A coalition that holds the round-0 slots of `H_L` and of all `m` contributing heights can compute every contributing beacon while it builds block `H_L`, and can therefore try list contents until the positions miss. This is full control of the draw, and it is the case the rule for `m` below excludes.

### Effect on detection (2 positions, `R` = 3,505 rows)

| forged rows `f` | one draw `p` | one colluding last slot `p^2` | two trailing slots `p^3` |
|---|---|---|---|
| all | 1 | 1 | 1 |
| 1,752 (half) | 0.750 | 0.562 | 0.422 |
| 350 (10 %) | 0.190 | 0.036 | 0.0068 |
| 35 (1 %) | 0.0199 | 0.00039 | 0.0000079 |
| 1 | 0.00057 | 0.00000033 | 2e-10 |

A Worker that skips the computation (every row wrong) gains nothing from a re-roll. A Worker that forges few rows was already rarely caught, and with one colluding validator it is caught about `1 / p` times less often again. The audit rate `s` multiplies all of these.

### Guidance for the parameters

- **`anchor_position_beacon_blocks` (`m`).** Set `m >= J`, where `J` is the longest run of consecutive round-0 proposer slots that any coalition below the fault threshold (one third of the voting power) can hold, computed by running the proposer rotation of the real validator set, and recompute it when the set changes. This excludes case 3. It does not remove case 1. For `N` validators of equal power a coalition below one third has at most `floor((N - 1) / 3)` members and can be adjacent in the rotation, so `J = floor((N - 1) / 3)`: the recommended `m` = 8 covers up to 27 equal validators, 33 is needed for 100, and the maximum 256 covers up to 771. With unequal power use the simulated value; many small validators lengthen the run.
- **`anchor_list_window_blocks` (`L`).** Do not shorten it to make placement unlikely. Placement fails only for `L` below about `1 / a` (20 blocks for a 5 % coalition), which no honest Worker could meet. Treat the placement of one colluding last slot as certain and size the deterrent on that.
- **Slash and audit rate.** For a Worker that may collude with a validator, take the detection of a forgery as `s * p^2` (or `s * p^(J+1)` for a coalition with run `J`) when comparing the expected penalty with the gain. The whole-inference cheat (`p` = 1) is unaffected.
- **Monitoring (no protocol change).** For every drawn audit (`EventAnchorPositionsDrawn`, `position_beacon_last_height`) record whether the last contributing blocks were committed in a round above 0, and by which skipped proposer. An honest network has a base rate of such blocks; a Worker whose audits show a higher rate, or a validator that is skipped mostly at such heights, is the signature of case 1.

### Options that would reduce it (protocol changes, not made)

- Fix the contributing heights for everybody at `list deadline + 1 .. list deadline + m` instead of relative to the list message. The Worker then cannot place a proposer: the last slot is colluding with probability about `a` instead of 1. The price is that every audit waits for the end of the list window.
- Attribute a block committed above round 0 at a contributing height to the proposer that was skipped, and act on a count of them. Restarting the window on such a block is not a remedy: a restart is itself a re-roll.
- A beacon that no single proposer can withhold (threshold or commit-reveal). Out of scope here.
