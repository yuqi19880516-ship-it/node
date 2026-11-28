# Changelog

## Unreleased

- **Verifier-panel audit of two-step tasks; one upgrade for the series.**
  - A task of a two-step Profile (anchor commitment schema version 2) may be
    audited by a Verifier panel. The verdict deadline written at selection
    covers the list window, the beacon heights, the draw grace, the Worker's
    windows and the panel schedule (rounds, publication, late window). With
    late submission the Worker's opening and proof deadlines are watched from
    the position draw.
  - A missing commitment list has no late form: one block after the list
    deadline the audit ends as the existing timeout (the task fails, the
    `anchor_timeout` fault, no pause, no late window, no batch forfeiture).
  - The task retention bound (`cleanup.task_terminal_summary_retention_blocks`
    above the life of an audit) counts the panel schedule when
    `anchor_audit_mode = 2`; under the validator vote it is unchanged.
  - Panel assignments carry the Profile's registration hash. A dispatch
    record that names a Worker paused for a late submission is refused like
    any paused Worker. While the per-model brake withholds a rejection's
    slash, the forfeiture of the Worker's batch waits with it: the batch is
    marked and not paid; a release that applies forfeits it, a release that
    discards lets it be paid. A Worker's open obligations include an audit
    waiting for a late submission and a batch that is not paid or forfeited.
  - One upgrade plan, `v0.6.4-two-step-sampling`, carries the whole series:
    task 4 -> 5 and hub 3 -> 4, both committing the parameter bodies again.
    The separate plan name `v0.6.3-verifier-panel` is gone.
- **Upgrade `v0.6.3-verifier-panel`.** The governance plan of this name
  activates the binary that carries the Verifier-panel audit. The task module
  goes to consensus version 5 and the hub module to 4. The task and service
  parameter commitments frame the fields added for the panel audit, so the
  commitments the previous binary recorded no longer match the same bodies:
  both migrations commit the unchanged body again as a new params version
  (the replaced task version is archived, so tasks in flight read the same
  body). No parameter value changes; every new parameter stays zero until
  governance sets it. New store prefixes live inside the existing stores.
- **Consensus-breaking: audit standing of panel Verifiers.** The chain keeps a
  standing per operator that sits on Verifier panels and applies three rules
  to panel eligibility. Each is off while its task parameters are zero (the
  default).
  - Allowance (`anchor_verifier_bond_multiple`,
    `anchor_verifier_allowance_window_blocks`): the value of the tasks an
    operator was drawn for inside the current allowance window plus the task
    at hand, times the multiple, must not exceed its effective bond. Windows
    are consecutive spans of the window length.
  - Missed votes (`anchor_verifier_miss_trigger_count`,
    `anchor_verifier_miss_window_blocks`, counted per
    `batch_settlement_period_blocks`): a member that cast no vote in a round
    that ran to its vote deadline, or cast UNAVAILABLE while every other
    member judged the material, missed. A period with a miss counts once. Each
    time the periods with a miss inside the window reach another multiple of
    the trigger count the operator is ineligible for the whole next
    settlement period. A member that had not voted when the other members
    decided the round early is not counted.
  - Wrong votes (`anchor_verifier_wrong_vote_slash_bps` set): a member whose
    PASS or REJECT is the opposite of a final verdict decided by a panel or a
    re-audit is deactivated and drawn for no further panel. UNAVAILABLE is
    never a wrong vote, and a verdict that comes from a missed deadline, an
    expired late window, unavailable material or no conclusion makes nobody
    wrong.
  - No slash is applied by this change for a missed or a wrong vote, and the
    re-audit opener receives no reward yet.
  - Query `VerifierAuditStanding` (`audited_value` of the current window,
    missed periods, `suspended_until_period`, `deactivated`,
    `allowance_limit`). New task store prefix `verifier_audit_standing`,
    exported as `verifier_audit_standings`.
- **Consensus-breaking: per-Worker, per-period batch settlement
  (`batch_settlement_period_blocks`) and panel fees.**
  - With the task parameter set (default 0 = off), a task joins the batch of
    (its Worker, `dispatch record height / batch_settlement_period_blocks`)
    when its dispatch is recorded. The task still settles on its own and every
    other payee and the user's refund are paid with it, but the Worker's net is
    held (in the `task_challenge_effect` module account) and added to the
    batch's `worker_due`.
  - When the period is over and every member task is final the batch is
    queued; EndBlock pays queued batches in one credit each, oldest first, ties
    by operator address, at most `batch_settlement_max_workers_per_block` per
    block (one when unset). A batch whose payment fails is taken off the queue
    with its held amount untouched and does not stop the others.
  - A member task that settles on a rejected audit forfeits the batch
    (`FORFEITED_CHEAT`); one that settles on an expired late window forfeits
    it as `FORFEITED_TIMEOUT`. The held amount and the Worker net of members
    that become final later go to the treasury. A batch that was already paid
    is not taken back. A missed deadline without a late window, panel silence
    and audits that end without a verdict forfeit nothing.
  - A member task's rows, the audit among them, are kept until its batch is
    paid or forfeited (evidence cleanup waits for it).
  - Panel fees. A task judged by a Verifier panel pays the panel from its
    verify share, in its own settlement plan, and its Worker no longer carries
    that share (a task the audit did not select, or whose audit drew no panel,
    is unchanged). Each drawn round other than the re-audit is worth one
    verify share, one seat per member: a member that voted is paid its seat
    less maintenance, the seat of a member that did not vote goes to the
    treasury. A second or third round is paid only while the budget reserved
    for the task covers a whole round fee. On a rejected task the panel is
    paid out of what would have been refunded to the user. The re-audit panel
    is paid from the opener's fee when the task settles; the unvoted part of
    the fee goes to the treasury.
  - `AnchorAuditState.settlement_period` is set at sampling for a member task.
  - Queries `WorkerBatch`, `WorkerBatches`, `WorkerBatchTasks`; events
    `EventWorkerBatchQueued`, `EventWorkerBatchSettled`,
    `EventWorkerBatchForfeited`. A batch that waits for a member task's late
    submission is reported as `SUSPENDED` with `awaiting_late_count`; both are
    computed when the batch is read.
  - New task store prefixes `worker_batch`, `worker_batch_task` (both exported:
    `worker_batches`, `worker_batch_tasks`), `worker_batch_task_index`,
    `worker_batch_close_index`, `worker_batch_queue` (rebuilt on import).
- **Consensus-breaking: Worker timeout and late submission in panel audits
  (`anchor_late_window_blocks`).** With the task parameter set (default 0 =
  off; it applies to audits with `anchor_audit_mode = 2` only), a timeout no
  longer ends an audit.
  - A timeout is recorded when the opening or the proof deadline passes, when
    the threshold of a panel casts UNAVAILABLE, or when the re-assigned panel
    round also reaches no conclusion. The audit then waits for a late
    submission until `late_deadline_height` (the recording height plus the late
    window), fixed at the task's first timeout.
  - While it waits, `MsgSubmitAnchorOpening` and `MsgSubmitAnchorProof` are
    accepted past their deadlines. Recording the proof (again, for a panel
    timeout; the same proof or another over the same opening, new locations
    allowed) is the late submission. It opens one more panel round, which is
    not re-assigned. A task has one late attempt.
  - Opening deadline, proof deadline and cast-UNAVAILABLE timeouts are the
    Worker's own: its service bond is marked `dispatch_paused` (reason bit 1)
    until its late submission or the end of the audit, the timeout is counted
    in its rolling ledger, and without a late submission by the late deadline
    the audit ends as `LATE_EXPIRED`: the task fails and its settlement
    applies the `anchor_timeout` fault (no jail).
  - Panel rounds that reach no conclusion are never the Worker's: no pause, no
    ledger entry, no fault. The Worker may record its proof again to have one
    more panel drawn; if it does not, or that panel reaches no conclusion or
    cannot be formed, the audit ends without a verdict and the Worker is paid.
  - Rolling ledger: one entry per settlement period
    (`batch_settlement_period_blocks`) with a timeout of the Worker's own,
    kept for `anchor_timeout_rolling_window_blocks`. The timeout that makes the
    count exceed `anchor_timeout_free_count`, once per period, is not open to
    late submission: it ends at once as a missed deadline with the
    `anchor_timeout` fault. With either parameter at 0 nothing is counted.
  - With `anchor_late_window_blocks = 0` the outcomes of the previous entry
    stand unchanged; an audit keeps the rule of the parameters its task was
    accepted under.
  - The verdict deadline of an audit with late submission also covers the late
    window and one more panel round. A panel audit may now have four rounds
    (numbers 2 to 5).
  - Query `WorkerLateSubmissions`; events `EventAnchorWorkerTimeout`,
    `EventAnchorLateSubmitted`, `EventAnchorLateExpired`,
    `EventWorkerDispatchPaused`, `EventWorkerDispatchResumed`.
  - New task store prefixes `worker_timeout_ledger` (exported as
    `worker_timeout_ledgers`) and `worker_late_index` (rebuilt on import).
  - The pause flag is set, cleared and reported (`Query/WorkerQuota`) by this
    change; it does not yet refuse a dispatch record that names a paused
    Worker.
- **Consensus-breaking: Verifier-panel anchor audit (`anchor_audit_mode = 2`).**
  With the task parameter `anchor_audit_mode` set to 2, the audit of a sampled
  anchor task is decided by a panel of Verifiers instead of the validator
  vote. The parameter defaults to 0; modes 0 and 1 behave exactly as before,
  and a task keeps the mode of the parameters it was accepted under.
  - Selection reuses the Verifier machinery: when the Worker's proof is
    recorded the chain freezes the operators eligible for the task (audit
    capability past its waiting epoch, fresh support, bond at the task's
    minimum stake, not the Worker, not a member of an earlier panel of the
    task, and not a block-producer identity where `min_validator_separation`
    is set), ranks them into a candidate window, accepts hand-raises relayed by
    a selected Task Builder (`MsgSubmitVerifierHandraises` with `verify_round`
    set to the panel round) and draws `anchor_panel_size` members by weight.
    Panel rounds are numbered from 2; round 1 remains the task's own round.
  - `MsgSubmitAnchorPanelVote`: each member casts one vote, PASS or REJECT
    over exactly the sampled positions, or UNAVAILABLE. A vote cannot be
    changed. A round closes when `anchor_panel_threshold` equal votes are cast,
    when every member has voted, or after `anchor_panel_vote_window_blocks`.
  - A PASS or REJECT round publishes a provisional verdict for
    `anchor_publication_window_blocks`. Nothing is paid or slashed before the
    verdict is final. `MsgRequestAnchorReaudit` lets any account pay for one
    re-audit by a panel of other members during that window: an opposite
    decision replaces the published verdict and returns the fee; anything else
    leaves the published verdict standing.
  - A first round without a conclusion is re-assigned once, automatically, to
    other members. Until late submission is implemented: a round in which the
    threshold of members could not obtain the Worker's material ends as
    material unavailable (the existing timeout fault, not paid, no jail); a
    re-assigned round that also reaches no conclusion ends the audit without a
    verdict (the Worker is paid, nobody is slashed). An absent vote is never
    read as a statement about the Worker.
  - Panel members are not paid by this change: the fee of each round is
    recorded on the round. The re-audit fee is held in the
    `task_challenge_effect` module account.
  - Queries: `AnchorPanelAssignments` (open rounds an operator must vote on),
    `AnchorAuditsUnderReview` (published verdicts that are not final), and
    `VerifierCandidateWindow` now also answers for an open panel round.
  - New task store prefixes `anchor_panel_selection`, `anchor_panel_due_index`,
    `anchor_panel_due_height`, `anchor_panel_member_index`,
    `anchor_under_review_index`, `anchor_panel_last_scan_height`. The working
    selection row of a round is not exported: after an import a round that was
    still choosing its members ends without a conclusion at the first block.
  - Parameter validation: with `anchor_audit_mode = 2`, `anchor_panel_size`
    must be 1..15 and `anchor_panel_threshold` a strict majority of it.
- **Consensus-breaking: registration hash in the Profile, input-token rule,
  per-model brake.** Three rules of the Verifier-panel contract. Each is off
  unless a Profile or a parameter turns it on.
  - **Registration hash.** The Worker anchor entry of a Profile's
    `required_infer_evidence` may carry `registration_hash`: the sha256 of the
    canonical `registration.json` its anchor audit is verified against. It is
    accepted only on that entry, as a non-zero 32-byte value, in
    `register-model-profile` JSON (`"registration_hash": "0x…"`) as well. An
    entry that sets it is framed with it as a fourth field, so the Profile's
    `evidence_schema_hash`, execution snapshot hash and registration body
    commit to it; an entry without it is framed and hashed exactly as before.
    `Query/Profile` returns it inside the evidence schema, and the Task keeper
    exposes `AnchorRegistrationHash(model_id, profile_version)`.
  - **Input-token rule** (`input_token_tolerance`, default 0 = off).
    `MsgRecordDispatch.input_tokens_attestations` may carry the user's signed
    statement of an order's input token count. With a non-zero tolerance in
    the parameters the task is recorded under, a statement that names the
    order, uses the order's signature scheme and signature chain id, and
    verifies as EIP-712 typed data (domain `TrueOpen Order Input Tokens`
    version `1`, type `OrderInputTokens(string chainId,bytes32 orderDigest,uint64 inputTokenCount,bytes32 attestationDigest)`)
    under the key that signed the order is kept with the task. A record
    without a statement, with two statements for one order, or with one that
    does not verify is accepted unchanged and the rule does not apply to that
    task; no dispatch record is refused and nobody is faulted for it. A
    takeover entry never carries one.
    A receipt of a task with a statement is refused when the prompt token
    count read from its Worker token commitment (`(size - 8) / 4 - generated`)
    is below `statement - tolerance`: `MsgSubmitInferReceipt` answers with
    status `REJECTED` and accepts nothing. The Worker may send another receipt
    before the inference deadline. If the deadline passes after such a
    refusal, the task ends as a refunded failure without the inference-timeout
    fault. New store prefixes `task_input_token_statement` and
    `task_input_token_refusal`; neither is part of the exported genesis.
  - **Per-model brake** (`anchor_brake_min_cases`, default 0 = off; needs
    `batch_settlement_period_blocks`). Per model and settlement period the
    chain counts the selected anchor audits settled (`audited`) and those that
    ended in a REJECT of the Worker's proof (`cases`; a missed deadline or
    unavailable material is an audit, not a case). The period trips when
    `cases >= anchor_brake_min_cases` and its rate is at least
    `anchor_brake_multiple_x1000 / 1000` times the mean rate of the preceding
    `anchor_brake_baseline_periods` periods that had audits and did not trip
    (the case count alone when there is none). From the tripping case on, the
    Worker slash of such a REJECT is withheld; the task still fails and the
    user is still refunded. `MsgReleaseAnchorBrake`, signed by the module
    authority, applies or discards the withheld slashes (at most 256 per
    message; repeat to continue). Later REJECT slashes of that period follow
    the release. `Query/AnchorBrake` returns the row (`period = 0` is the
    current period). Events `EventAnchorBrakeTripped` and
    `EventAnchorBrakeReleased`. Rows are exported in `anchor_brakes`; the
    withheld slashes are not part of the exported genesis.
    `anchor_brake_baseline_periods` is bounded at 64. Suggested values are
    published as `RecommendedAnchorBrakeMinCases` (3),
    `RecommendedAnchorBrakeMultipleX1000` (3000) and
    `RecommendedAnchorBrakeBaselinePeriods` (7).
- **Consensus-breaking: Worker bond floor, order quota and unbonding
  conditions.** Four Hub service parameters that were carried but not read are
  now applied. Each is off at its zero default, and a chain that sets none of
  them stores and answers exactly what it did before.
  - `worker_bond_floor_by_tier`: a dispatch record whose winner has an
    effective bond below the floor of the Profile's resource tier is refused.
    Tiers are listed once, in increasing order, within 1 to 5, with a non-zero
    amount.
  - `worker_quota_period_value_bps_of_bond` together with the Task parameter
    `batch_settlement_period_blocks`: the order value a Worker has reserved and
    settled in one settlement period may not exceed
    `effective_bond * bps / 10000`. The period of a task is
    `record_height / batch_settlement_period_blocks` under the task's own
    frozen parameters. A dispatch record reserves the task's order value; the
    record that does not fit is refused. At finality the reservation is
    replaced by the order value of the tokens the settlement billed (zero for
    a task that fails before verification). The coefficient is bounded at
    100000. Without a settlement period there is no quota.
  - A dispatch record is also refused while the winner's bond has
    `dispatch_paused` set.
  - These three refusals reject the new record with `WINNER_NOT_ELIGIBLE` and
    do nothing else: no ineligibility receipt, no fault, no change to any task
    already recorded. The existing ineligibility outcomes (tombstoned, fully
    unbonding, bond below the Profile minimum, liability not covered) are
    evaluated first and are unchanged.
  - `stop_dispatch_on_unbond_request`: a service unbonding request sets the
    pause reason `UNBONDING` (bit 2) on the bond; it is cleared when the
    operator's pending unbonding total returns to zero.
  - `unbond_requires_no_open_tasks`: a matured unbonding is not withdrawn
    while the operator is the Worker of a task that is not final.
  - `hub.v1.Query/WorkerQuota` answers the period (0 in the request is the
    current one), the quota at the current effective bond, the reserved and
    settled value, what is left, and `enforced` (true when both the
    coefficient and a settlement period are set).
  - Quota rows are exported and imported in Hub genesis field `worker_quotas`
    and deleted once they hold no reservation and lie 16 periods behind the
    operator's newest record. New store prefix `worker_quota`.
  - Suggested values are published as `RecommendedWorkerQuotaPeriodValueBpsOfBond`
    (1428), `RecommendedWorkerBondFloorRevenuePeriods` (7) and
    `RecommendedServiceUnbondingPeriodBlocks` (1209600). No default changed.
  - A task's terminal transition (settlement, or failure before verification)
    now reads the task's frozen parameters to find its settlement period. On a
    chain without a settlement period this is the only difference: one more
    read in those transactions, no state change.
- **Consensus-breaking: audit capability for Verifier panels.** A Cortex
  operator may declare `audit_capability` for a model in
  `MsgDeclareModelSupport`, alone or together with inference or verification
  capability. Nothing draws on it yet; the panel audit that does follows.
  - An operator that declares only audit capability needs a service bond at or
    above the model's `support_min_stake`, as for any support. Its support row
    is declared and fresh but never active: it carries no support vote weight
    and does not count towards a model's active support.
  - The capability is usable from the epoch after the one that switched it on
    (a new declaration, a lapsed one made again, or the flag added to a live
    row). No served task is needed first.
  - An audit-only row is kept fresh by the daily support confirmation
    (`MsgBatchConfirmModelSupport`, signed by the service key and relayed like
    any other): while the declaration stands, the bond covers the minimum and
    the capability is audit, a confirmation moves freshness to
    `support_window_epochs` from its epoch. It never activates the row and the
    waiting epoch is not served again. Repeating the declaration renews it as
    well. The refresh rule for inference and verification support is
    unchanged: a row that never served a task is still skipped, with or
    without the audit flag next to it.
  - With the service parameter `min_validator_separation` set, a declaration
    with audit capability is refused when the operator account or its service
    address is the operator account of a bonded consensus validator or an
    address registered to cast a validator's audit vote, or when the service
    key is a bonded validator's consensus key. The parameter is unset by
    default; a network that turns panel audit on should set it.
  - New store prefix `audit_capability_declared_epoch`. It is not part of the
    exported genesis: after an export and import a capability declared in the
    export epoch is usable at once instead of one epoch later.
  - Declarations without audit capability behave exactly as before.
- **Two-step sampling of the anchor audit (off by default).** A Profile
  version whose anchor evidence requirement carries
  `commitment_schema_version = 2` is audited in two steps: the first beacon
  only selects the task; the selected Worker records the hash of its per-row
  commitment list with `MsgSubmitAnchorCommitmentList`; the positions are then
  drawn in EndBlock from the aggregated proposer-VRF beacons of the
  `anchor_position_beacon_blocks` heights after the block that included the
  list. Opening and proof deadlines count from the draw.
  - The first accepted list is final: the same hash and size again is a NOOP,
    any other content is refused for the rest of the audit, and the message is
    refused after `list_deadline_height`.
  - No list by the deadline ends the audit REJECT with source `TIMEOUT`, settled
    as the existing timeout fault (the fault of a missing opening: the smaller
    slash, no jail, Task not paid). Everything the chain owes the audit can only
    delay it or end it NO_VERDICT: a contributing beacon that is missing or is
    not a proposer-VRF value restarts the window with fresh heights, and a draw
    that could no longer leave the full opening, proof and verdict windows is
    not made.
  - New parameters `verification.anchor_list_window_blocks` and
    `verification.anchor_position_beacon_blocks`, both zero by default
    (recommended 1,800 and 8). No `Default*` value changes. Registering a
    version-2 Profile is refused unless both are set and
    `beacon.vrf_required_from_height` is set and reached.
  - Profiles of schema version 1, unselected tasks and audits in flight are
    unchanged: a one-step audit row is byte for byte the previous one. The Task
    parameter commitment changes because it frames every field. Two new store
    prefixes inside the Task store (the sampling queue); no store key change.
- **Two-step sampling: fixes after the release-candidate audit.**
  - The chain gives its own position draw a grace of 256 blocks
    (`AnchorPositionDrawGraceBlocks`, a protocol constant). The verdict deadline
    written at selection is now `list deadline + beacon blocks + 256 + opening +
    proof + verdict windows`, and the draw is made as long as the three full
    windows still end by it. A list recorded in the last block of the list
    window no longer ends NO_VERDICT after one block of EndBlock delay or one
    restarted beacon window. No window of the Worker is shortened; the list
    deadline is unchanged; audits selected before the change keep the deadline
    they carry.
  - Parameter validation now compares the task retention
    (`cleanup.task_terminal_summary_retention_blocks`) with the real longest
    life of an audit: list window + beacon blocks + 256 + opening + proof +
    verdict windows, where the list window and beacon blocks are the configured
    ones or, when the body carries none, the recommended 1,800 + 8 that a task
    of a two-step Profile then runs on. Before, the fallback windows were not
    counted. A body recorded under the earlier rule stays valid as history and
    keeps its commitment; a parameter update or a genesis whose retention is
    not above the new bound is refused.
  - `MsgSubmitAnchorCommitmentList` with the recorded hash and size and
    another `list_uri` now corrects the recorded location (status APPLIED,
    `EventAnchorCommitmentListRecorded` emitted again with unchanged fields)
    while the audit has no final verdict: before and after the draw and after
    the list deadline. The new location must lie under the frozen
    `da_uri_prefix`. It changes nothing but `commitment_list_uri`. The hash and
    the size stay final: any other content is refused as before. With the
    recorded location, or after the verdict is final, the message is a NOOP.
    Before, a wrong first location could never be corrected.
  - A missing commitment list on a round that did not pass is applied at the
    same moment as a missing opening: only by a settlement at or after the
    verdict deadline. Before, it took the timeout fault from the block after
    the list deadline, where the one-step counterpart imposes nothing. The
    audit row still records REJECT / TIMEOUT one block after the list deadline,
    and a PASS round is decided by it from that block as before.
  - A chain that stops requiring the VRF beacon after a two-step Profile was
    registered can make no position draw. Such an audit now ends at its first
    draw visit as NO_VERDICT (source `TIMEOUT`) with a reason of its own that
    names `beacon.vrf_required_from_height`, instead of restarting windows
    until none fits: the Worker is paid, nothing is slashed. A draw whose
    beacons are verified proposer-VRF values is made as before.
    `MsgUpdateHubParams` now refuses to set `beacon.vrf_required_from_height`
    to zero, or above the current height once it has been reached, while a
    Profile version that is not delisted uses two-step sampling.
  - `docs/runbooks/anchor_two_step_sampling.md`: the heights of one audit, the
    longest audit life a Worker's retention must cover, the list message rules,
    the upgrade, and the stated residual of the position beacon (the last
    contributing proposers can re-roll the draw by withholding a block, and the
    Worker chooses which proposers those are) with guidance for
    `anchor_position_beacon_blocks`. The protocol is not changed for it.
  - **Upgrade plan `v0.6.4-two-step-sampling`; Task module consensus version
    4 -> 5.** The Task parameter commitment frames every field, so the fields
    added since the anchor audit release (18..43) change the hash of an
    unchanged body: after an in-place upgrade the stored commitment no longer
    matched, the parameter invariant failed and genesis export was refused until
    the next governance update. The 4 -> 5 migration writes the live body as a
    new params version with its commitment and archives the replaced version
    unchanged (rows bound to it keep reading it). A task retention that is not
    above the new bound is raised by the migration, with a logged note to
    confirm by governance. A chain whose commitment already matches is left
    alone. A genesis exported by the previous binary still carries the old
    commitment and is not accepted as it is by a cold start of this binary.
- **Consensus-breaking: anchor audit, material the Worker does not keep
  retrievable.** A validator that cannot obtain the opening, proof or manifest
  a Worker recorded may vote `UNAVAILABLE`.
  - The vote is accepted only in the second half of the verdict window (from
    `proof_deadline_height + (verdict_deadline_height - proof_deadline_height) / 2`);
    an earlier one is refused. It never closes an audit early, and the same
    validator may replace it with PASS or REJECT while the window is open.
  - PASS or REJECT at two thirds of the bonded voting power decide as before.
    At the verdict deadline an undecided audit is read on the votes as they
    stand, at current power: PASS above one third ends it NO_VERDICT (the
    material was obtainable); otherwise REJECT and UNAVAILABLE together at two
    thirds end it REJECT with source `MATERIAL_UNAVAILABLE`; otherwise
    NO_VERDICT.
  - `MATERIAL_UNAVAILABLE` settles as the existing timeout fault: the Task is
    not paid, the smaller slash, no jail. Silent validators still end an audit
    NO_VERDICT, which pays the Task and slashes nobody. Before this change a
    Worker could record an opening and a proof, keep them out of reach, and be
    paid in full.
  - All validators must switch to the new binary at the same height. There is
    no store migration and no parameter change. Audits in flight at the upgrade
    accept the new vote value.
- **A Worker publishes where it serves audit material from.** A Cortex
  operator's service descriptor may carry an `ANCHOR_DA` endpoint (kind 4): an
  `http://` or `https://` URL ending in `/`. When a Task is selected for audit,
  the endpoint its Worker's descriptor carries at that height is frozen into
  the audit as `da_uri_prefix`, and `MsgSubmitAnchorOpening` and
  `MsgSubmitAnchorProof` are refused unless every location they record lies
  under it. A Worker that has published none is not restricted, so Workers
  already serving and audits in flight are unaffected until the operator
  publishes one.
- Recorded audit locations and the `ANCHOR_DA` endpoint must be `http://` or
  `https://` URLs of printable ASCII with a host, an optional port in
  1..65535, no userinfo and no fragment. `http://a:99999/x`, which one URL
  parser accepts and another raises on, is refused.
- A recorded proof may be at most 256 MiB. The opening size has only a sanity
  ceiling (1 TiB): the bound an honest opening has to respect is a property of
  the Profile's registration and is applied off chain.
- A selection row left behind by a Task that the verify-open deadline already
  failed and refunded is dropped as stale. It used to return an invariant error
  out of EndBlock one block after the assignment deadline whenever the selection
  beacon had stayed unavailable until then.
- Hub: `beacon.vrf_required_from_height` can no longer be moved to a height the
  chain has already reached.
- **Consensus-breaking: value-evidence window.** A round closes as soon as every
  selected Verifier has revealed, which can be before its reveal deadline. The
  value-evidence window now runs `verifier_value_evidence_window_blocks` from
  that close instead of from the reveal deadline, so such a Task settles up to
  one reveal window sooner (600 blocks with the stock seeds). The last reveal
  still has the full window to be contradicted. A round closed on a missing
  reveal keeps the window it froze, a challenge round cuts the window the same
  way, and a Task whose round closed before the upgrade keeps its longer window;
  the Task value-evidence invariant accepts both shapes.
  - All validators must switch to the new binary at the same height. There is no
    store migration and no parameter change.
- The stock Genesis seeds change three Task and Hub values. They only affect a
  chain started from them; a running chain keeps the parameters it has.
  - `objective_forgery_slash_bps` from 300 to 30. A Worker or Verifier duty
    reserves the largest of the minimum liability, the order value and
    `effective_bond * bps`. With the seed bonds the last term decides, so each
    duty reserves a tenth as much and one operator can hold about ten times as
    many duties at once. The slash for a confirmed objective forgery is
    `reserved * bps`, so for those bonds it falls by about a hundredfold.
  - `challenge_open_bond` from 0 to 100000. A challenge round that agrees with
    round 1 now forfeits the opener's bond; one that disagrees refunds it.
  - `max_order_sequences_per_active_session` from 4096 to 65536.
- **Consensus-breaking: anchor-only verification.** A Profile version that
  requires the Worker anchor commitment is now verified by the anchor audit
  alone. Its Tasks draw no Verifier: the chain samples each one from the beacon
  at the same height and rate as before, a sampled Task is opened and proved by
  the Worker and decided by the validators, and an unsampled Task settles on the
  Worker's receipt. The share of the price a Verifier round would have taken is
  paid to the Worker. Profile versions without that commitment are unchanged.
  - This changes every Profile version that already requires the commitment,
    not only versions registered later: from the upgrade height on, new Tasks
    on those versions stop drawing Verifiers.
  - All validators must switch to the new binary at the same height. There is
    no store migration and no parameter change.
  - A Task whose receipt was accepted before the upgrade keeps the Verifier
    path it froze, including its challenge window.
- An anchor-only Task has no challenge round: `MsgOpenChallengeRound` is refused
  for it, so an unsampled Task of that kind has no dispute path.
- The billed token count of an anchor-only Task is the count in the Worker's
  signed receipt. No Verifier recounts it. A sampled audit does not detect a
  receipt that counts trailing prompt tokens as generated ones; the excess is
  bounded per Task by the prompt length and by the order's `max_output_tokens`.

## v0.2.0

Phase 1 is a coordinated breaking release for a fresh genesis. Retained Phase 0
state, Sessions and parameter commitments are not supported. See
[Wire v0.6.0](https://github.com/TrueOpen/wire/releases/tag/v0.6.0) for the
released protocol contract.

- Start inference on signed off-chain dispatch while independent chain
  admission records the accepted attempt. Preserve attempt-bound output,
  Builder replication, takeover and failure recovery.
- Prefund Sessions and support delegated order authorization, delayed
  withdrawals, cancellation and key changes.
- Enforce compensation, objective dispatch challenges, bounded verification
  evidence, frozen Task/Session policy and epoch snapshot scoring.
- Preserve service-key proof authority across rotation and emergency
  revocation, with bounded Genesis recovery and terminal cleanup.
- Pin the immutable Wire v0.6.0 module, descriptor and registries; synchronize
  the published token-ID fixture without adding display statistics to chain
  storage, validation or accounting.
- Build release binaries with Go 1.25.10, matching the module and contract CI.
- Fix `multi-node` initialization to render the custom Task Event gRPC and
  beacon settings from the shared application defaults instead of panicking
  with the SDK base configuration, preserve the requested minimum gas price,
  and reuse the existing DIRECT-signature preprocessing hook for valid gentxs.
