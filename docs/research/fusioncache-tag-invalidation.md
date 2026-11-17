# FusionCache tag invalidation: inspiration, not equivalent guarantees

## Scope and source pin

Investigated upstream `ZiggyCreatures/FusionCache` at **`52b14c40f007d20e6bd702bf01756b2dd9fe015d`**, commit subject “Better tests”, committer date **2026-11-10T14:38:51Z**. This was `HEAD` returned by `gh api repos/ZiggyCreatures/FusionCache/commits/HEAD` during this research; [immutable commit metadata][commit]. Repository files were read through `xd://github` `file_read`, including its full-source artifacts where responses elided middle sections. No implementation changes, builds, tests, or runtime experiments were performed.

**Bottom line:** FusionCache demonstrates backend-independent, lazy logical invalidation using cached **wall-clock timestamp barriers**, not durable generations. Its implementation is useful inspiration for avoiding Redis Cluster cross-slot deletion, but does **not** establish our accepted stronger L2 ordering, metadata-loss safety, or reclamation guarantees.

## What the implementation actually does

### Marker creation, reading, and caching

- `RemoveByTag(tag)` calls `SetTagDataInternal(tag, GetCurrentTimestamp(), options)`, which performs an ordinary `Set<long>` with no tags. Default marker keys are `__fc:t:<tag>`, additionally processed by the configured cache-key prefix. There is no reverse membership index or enumeration of tagged data. [Creation][create], [internal strings][strings], [prefix handling][prefix].
- Values carry their creation timestamp and tag array through memory and distributed envelopes. Validation checks **`entry.Timestamp <= markerValue`**: equality invalidates too, despite the official explanation describing a strictly greater marker. It obtains markers through normal `GetOrSet<long>` using `TagsDefaultEntryOptions`, so an L2 data read need not fetch the latest marker from L2. Internal marker keys bypass secondary validation to prevent recursion. [Validation owner][validate], [distributed envelope preservation][envelope], [official tagging explanation][tag-doc].
- On a marker miss, `SharedTagExpirationDataFactory` returns the stale marker value if available, otherwise **zero**. It disables backplane notifications; when the result is zero it also skips the distributed write. Thus negative knowledge is cached locally, and an existing stale nonzero marker can be renewed through ordinary caching. Missing metadata does not mean “reject the data.” [Factory owner][marker-factory].
- Defaults: marker L1 logical duration **1 hour**, L2 logical duration **24 hours**, fail-safe enabled with **10-day physical lifetime**, no background distributed/backplane writes, no distributed/serialization/backplane rethrow flags, and memory priority `NeverRemove`. These are configurable defaults, not a lifetime derived from every associated data entry. L2 physical expiry is computed using the fail-safe maximum (at least the logical duration). `NeverRemove` is an L1 hint, not protection against Redis eviction. [Defaults][defaults], [physical TTL owner][physical-ttl].

### Write ordering and distributed validation

- Ordinary `Set` creates a timestamped entry, writes L1, then invokes distributed operations. The distributed action performs the L2 write **before** publishing the backplane notification. It is not an atomic transaction across data and tag markers. `RemoveByTag` writes its marker via this same path; there is no tag-marker read/compare during a tagged `Set`. [Set owner][set], [distributed ordering][ordering].
- Entry timestamps and invalidation values come from **`DateTimeOffset.UtcNow.UtcTicks` on the application node**, not Redis time or a shared sequence. Only the two process-local Clear timestamps use `AdvanceTimestamp`, an atomic max operation. There is no corresponding distributed max/CAS in the marker `Set` path. [Clock and atomic max][clock], [creation][create], [backend write][backend-write].
- Foreground factory execution captures its entry timestamp **before** invoking the factory and preserves it when creating the result entry. This is a source fact, not a universal factory-start ordering contract; background completion follows a separate entry-creation path. Our accepted requirements explicitly do not require factory-start ordering. [Foreground factory owner][factory].
- Both get-or-set and read-only paths call secondary validation after obtaining entries from L1/L2. The distributed accessor itself only deserializes and checks logical expiry: tag validation belongs to the FusionCache layer above it. Read-only validation requests cascading removal/expiry; get-or-set validation can retain an invalid entry as a stale factory fallback. [Get-or-set caller][getorset], [read-only caller][readonly], [accessor][backend-read].

### Clear, L1, and backplane

- `Clear(true)` advances a local Clear-expire timestamp and writes a special `*` marker; `Clear(false)` advances Clear-remove and writes `!`. These barriers apply to entries **without tags too**. Clear-remove discards the candidate entirely; Clear-expire can retain it for fail-safe. A raw L1 clear is used only when there is no L2/backplane and the owned memory cache can safely clear. [Clear owner][clear], [validation][validate], [raw-clear conditions][raw-clear], [official Clear docs][clear-doc].
- Clear timestamps persist in two process-local fields. Validation loads the marker initially; without a backplane but with L2 it asks the marker cache again on each validation (which can still hit cached L1 marker data). With a backplane, already initialized Clear fields generally rely on notifications rather than reloading L2 every read. The local atomic max prevents these two fields from regressing, but does not make the shared marker durable. [Validation branches][validate], [atomic max][clock].
- A marker `EntrySet` notification is special-cased: the receiver advances its Clear field if applicable and writes marker data directly into L1 with distributed writes and further notifications disabled. It does **not** enumerate all associated L1 entries. Later access discovers their invalidity. Notably, receivers use the notification's entry timestamp as the marker value; the sender's stored marker value was captured just before its ordinary marker entry was created. [Backplane dispatch and receiver][backplane], [skip helper][skip-helper].
- Official docs acknowledge an incoherence window without a backplane and suggest reducing both ordinary-entry and marker L1 durations. This is not an authoritative marker read on every L2 access, nor a synchronous acknowledgement that every remote L1 processed the notification. [Official coherence guidance][tag-doc].

### Cleanup, infinite lifetime, and errors

- Invalidation initially writes **one marker**, not deletes all matching bytes. On a later read-only access, the secondary checker calls `RemoveInternal` or `ExpireInternal`; both ultimately remove that data key from L2 and publish an appropriate notification. Get-or-set instead recomputes/overwrites, retaining stale candidates when allowed. Untouched finite-lived bytes rely on ordinary expiry. There is no tag-driven background enumeration/reaper in these owners. [Cascade branches][validate], [remove/expire distributed owners][remove-expire], [official passive design][tag-doc].
- Current upstream defaults to **`RemoveByTagBehavior.Expire`**, not hard removal. Factory failures may therefore return a tag-invalidated stale value; read-only `AllowStaleOnReadOnly` can also return stale candidates. `RemoveByTagBehavior.Remove` and Clear-remove discard the candidate instead. Thus “invalidated” is not universally equivalent to “unreadable bytes.” [Default][defaults], [checker][validate], [factory fallback][factory], [read-only stale return][readonly].
- FusionCache does **not** use our Go `TTL=0` convention for permanence. With fail-safe disabled, a distributed duration `<= 0` takes the removal path. For no expiration, `TimeSpan.MaxValue` is normalized to `DateTimeOffset.MaxValue`; the accessor passes an absolute expiration to its provider. This is effectively unbounded retention, not proof of a provider-level Redis `TTL=0` representation. No mechanism here guarantees timely reclamation of invalidated, effectively unbounded entries that are never read again. [Zero-duration owner][backend-write], [unbounded normalization][unbounded], [physical expiration][physical-ttl].
- Marker operations normally wait for distributed work, **but default I/O errors are swallowed**, failed writes can queue auto-recovery, and marker-read failures can become misses/stale fallbacks. Consequently a normally returned `RemoveByTag` is not proof that L2 accepted its marker. Auto-recovery is deferred, local state with expiry/retry/queue limits, not a transactional acknowledgement. [Defaults][defaults], [write failure handling][backend-errors], [read errors][backend-read], [queue scheduling][ordering], [recovery expiry][recovery], [official recovery cleanup][recovery-doc].
- Exception nuance: although marker defaults set `ReThrowSerializationExceptions=false`, this pinned source's **write serialization path unconditionally throws** (the condition is commented out). Read deserialization does honor the flag. Cancellation is rethrown. Do not infer “never throws” from the defaults table. [Sync serialization][backend-write], [sync read handling][backend-read], [async owner][async-backend].

## Guarantee assessment against our accepted constraints

The following are **inferences from the cited control flow**, not upstream promises or experimentally reproduced bugs:

| Accepted constraint / concern | Assessment of a direct timestamp-barrier port |
| --- | --- |
| A successful invalidation hides tagged Sets completed before invalidation began from subsequent L2 reads | **Not established.** A writer whose clock is ahead can have `entry.Timestamp > invalidator.Timestamp` even when its Set completed first. Cached zero/old marker values, swallowed L2 failures, or optional background writes create additional gaps. Overlapping Sets may survive or invalidate under our contract; that relaxation does not fix completed-before clock skew. |
| Marker expiry/eviction/recreation must not resurrect old entries | **Not satisfied by this mechanism alone.** Consider a long-lived, unread entry, a successful tag invalidation, disappearance of the shared marker, then a fresh node: the marker factory supplies zero, so the original entry can pass validation. A warm node's cached marker/Clear field can mask the issue, but cannot repair a fresh node. Recreating a marker as zero is not an epoch change. |
| Concurrent/reordered marker updates | **No distributed monotonicity established.** Marker writes are ordinary replacement Sets. A delayed older write can replace a newer barrier; separate local Clear max fields do not protect the shared marker. Cross-node wall-clock rollback/skew adds another source of regression. |
| TTL=0 tagged values remain supported, with eventual reclamation even if never read again | **Not provided by passive invalidation alone.** Finite marker TTLs can expire before effectively unbounded data; making markers permanent only preserves knowledge, not reclaims unread data bytes. Reclamation requires a separately designed lifecycle/index/worker, not an assumed property of the timestamp technique. |
| Keep one cache namespace spread across Cluster shards | **Useful fit.** Independent single-key marker/data operations do not inherently require co-locating the entire namespace. The cost is metadata reads and an explicitly designed consistency protocol, not a free atomic cross-slot barrier. |
| L2-only scope; remote-only L1 limitation remains separate | **Different integration boundary.** Upstream validates above L1/L2 and owns backplane/fail-safe behavior. Our Redis adapter can wrap opaque bytes and validate within `Get` using the existing tags argument to `Set`, but upstream's whole-cache behavior is not automatically inherited. No core interface change is implied by this research. |
| New storage namespace and cold cutover allowed | **Helpful but insufficient.** A new envelope/metadata namespace avoids interpreting legacy raw payloads as new records; mixed old/new versions still cannot be said to share tag invalidations. This does not solve ongoing marker loss, ordering, or reclamation. |

Sources for the table are the implementation owners above, particularly [validation][validate], [marker miss behavior][marker-factory], [clock][clock], [write ordering][ordering], and [TTL conversion][physical-ttl]. The local interface is `l2/adapter.go`: `Set(ctx, key, value, ttl, tags)`, with `TTL=0` meaning indefinite and optional `TagsDeleter`; the adapter owns opaque bytes, unlike upstream FusionCache's integration layer.

## Redis-specific limits and remaining unknowns

- **No tag-specific Redis scripts or cross-slot transaction are required by upstream's algorithm.** Its distributed accessor calls only provider `Get`, `Set`, and `Remove`, one logical key at a time. A particular `IDistributedCache` Redis provider may internally use Lua/multiple structures; that provider implementation was not audited, and no claim of “zero Lua anywhere” is made. [Accessor writes][backend-write], [reads][backend-read], [removals][remove-backend].
- Our existing arbitrary-member deletion script is not rescued by this inspiration: Redis requires scripts to declare every accessed key explicitly, and Cluster multi-key work normally requires one hash slot. Logical invalidation can avoid that **particular** cross-slot deletion requirement; any replacement scripts still need their own declared-key/slot proof. [Redis EVAL rules](https://redis.io/docs/latest/commands/eval/), [Cluster key distribution/multi-key rules](https://redis.io/docs/latest/operate/oss_and_stack/reference/cluster-spec/#implemented-subset).
- Marker loss through expiry/eviction and **rollback to an older non-missing value** are distinct. Redis `allkeys-*` policies can evict metadata independently of data. Redis Cluster also explicitly permits acknowledged writes to be lost on failover. Even a fresh-generation-on-miss scheme would need an explicit storage-consistency assumption or additional design to handle an acknowledged invalidation rolling back while old data survives. Do not promise unconditional no-resurrection across Redis history loss. [Eviction policies](https://redis.io/docs/latest/develop/reference/eviction/#eviction-policies), [Cluster write safety](https://redis.io/docs/latest/operate/oss_and_stack/reference/cluster-spec/#write-safety).
- **Unresolved, not supplied by upstream:** a clock-independent completed-Set ordering protocol; authoritative read/replica consistency assumptions; fail-closed metadata-loss/recreation behavior; retention coupling or safe retirement; and eventual cleanup/lifecycle ownership for indefinitely stored, never-read entries. These must be answered for our adapter before implementation approval. Upstream's official docs describe intended user-visible behavior and resiliency, but the reviewed sources do not establish those stronger guarantees under arbitrary skew, metadata loss, or Redis rollback.

[commit]: https://github.com/ZiggyCreatures/FusionCache/commit/52b14c40f007d20e6bd702bf01756b2dd9fe015d
[tag-doc]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/docs/Tagging.md
[clear-doc]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/docs/Clear.md
[recovery-doc]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/docs/AutoRecovery.md
[create]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/FusionCache_Sync.cs#L1136-L1189
[strings]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/FusionCacheInternalStrings.cs#L27-L46
[prefix]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/FusionCache.cs
[validate]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/FusionCache_Sync.cs#L999-L1133
[envelope]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/Internals/Distributed/FusionCacheDistributedEntry.cs#L101-L126
[marker-factory]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/Internals/FusionCacheInternalUtils.cs#L568-L592
[defaults]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/FusionCacheOptions.cs#L70-L105
[physical-ttl]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/FusionCacheEntryOptions.cs#L1087-L1122
[set]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/FusionCache_Sync.cs#L845-L885
[ordering]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/FusionCache_Sync.cs#L1274-L1375
[clock]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/Internals/FusionCacheInternalUtils.cs#L78-L102
[factory]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/FusionCache_Sync.cs#L335-L415
[getorset]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/FusionCache_Sync.cs#L137-L153
[readonly]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/FusionCache_Sync.cs#L598-L732
[backend-read]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/Internals/Distributed/DistributedCacheAccessor_Sync.cs#L145-L290
[clear]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/FusionCache_Sync.cs#L1223-L1268
[raw-clear]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/FusionCache.cs
[backplane]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/Internals/Backplane/BackplaneAccessor_Sync.cs#L301-L453
[skip-helper]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/FusionCacheEntryOptions.cs#L894-L899
[remove-expire]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/FusionCache_Sync.cs#L1383-L1424
[unbounded]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/Internals/FusionCacheInternalUtils.cs#L444-L465
[backend-write]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/Internals/Distributed/DistributedCacheAccessor_Sync.cs#L60-L142
[backend-errors]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/Internals/Distributed/DistributedCacheAccessor_Sync.cs#L9-L57
[recovery]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/Internals/AutoRecovery/AutoRecoveryService.cs#L55-L111
[async-backend]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/Internals/Distributed/DistributedCacheAccessor_Async.cs#L108-L152
[remove-backend]: https://github.com/ZiggyCreatures/FusionCache/blob/52b14c40f007d20e6bd702bf01756b2dd9fe015d/src/ZiggyCreatures.FusionCache/Internals/Distributed/DistributedCacheAccessor_Sync.cs#L293-L320

## Decisions from the issue #37 design discussion

Accepted by the maintainer:

- Keep one cache namespace's payloads distributed across Cluster shards.
- Permit logical invalidation and eventual physical reclamation.
- A successful invalidation must fence tagged Sets completed before it began from subsequent L2 reads. Overlapping Sets may survive or be invalidated; factory-start ordering is not promised.
- Metadata expiry/eviction must not resurrect invalidated records.
- Preserve `TTL=0` entries; reclaim invalidated entries eventually even when never read again.
- Permit a versioned storage namespace and cold upgrade; old/new layouts do not share invalidation.
- Keep this issue L2-scoped; remote-only L1 invalidation remains separate.
- Exclude Redis acknowledged-write rollback/restore from the guarantee; use authoritative primary validation.
- Applications own a cancellable maintenance loop; the adapter does not own the injected Redis client's lifetime.
- Permit logical `Clear` plus eventual reclamation, including validation overhead for untagged entries.
- Accept a shared authoritative metadata shard while payloads remain sharded.
- Investigate automatic recovery rather than settling for manual cutover after metadata loss.

### Alternatives considered

- **Timestamp markers:** useful upstream inspiration, but lack the required clock-independent ordering and metadata-loss safety.
- **Independent generation markers:** need a retained allocator or a safe incarnation protocol; resetting counters or replaying random request tokens can restore a retired identity.
- **Fixed hash buckets containing data and metadata:** avoid independent eviction within a bucket, but still need incarnation fencing for delayed writes after bucket loss; native hash-field TTL adds a Redis 7.4 requirement.
- **Physical sweep alone:** insufficient during migration. A record can move from a not-yet-scanned primary to an already-scanned primary. Use repeated sweeps for eventual reclamation, not as the validity fence.

### Automatic recovery experiment

**Candidate, not approved implementation:** one control hash contains an incarnation identity, retained sequence, Clear generation, and per-tag generations. Data wrappers capture incarnation and generation snapshots once per logical Set. All retries of its data write reuse those bytes. Missing/mismatched metadata means a miss, never a default-generation match.

When the whole control hash disappears, a single-key initialize-if-absent script may install a **fresh incarnation**. It must execute at most once for each candidate identity. After an ambiguous result, a new explicit attempt uses a new identity; it never resends the previous candidate. Concurrent initializers adopt the winning control record. Once a hash is present, initialization must not reset it.

Why this matters: if a candidate was installed and used by data records, its one permitted execution has already happened. It cannot execute again after later metadata loss. A candidate whose only execution was delayed has never previously been installed and used. Fresh candidate identities assume cryptographically strong randomness and negligible collision probability; this is not an absolute uniqueness proof.

**Pinned go-redis evidence:** `v9.18.0`, commit `90faf0692313160b1140f96716763227912aec44`.

- Base-client command retries run inside `baseClient.process`; constructor `MaxRetries=-1` normalizes to zero retries. [Loop](https://github.com/redis/go-redis/blob/90faf0692313160b1140f96716763227912aec44/redis.go#L686-L738), [normalization](https://github.com/redis/go-redis/blob/90faf0692313160b1140f96716763227912aec44/options.go#L384-L389).
- Cluster processing has a separate outer retry/redirection loop, including network-error retries. Disabling node retries alone does **not** make `ClusterClient.Eval` single-attempt. [Cluster loop](https://github.com/redis/go-redis/blob/90faf0692313160b1140f96716763227912aec44/osscluster.go#L1213-L1313).
- `MasterForKey` exposes the underlying primary client. Direct execution on that client bypasses the outer Cluster loop, provided the node client has zero retries and its hooks do not replay commands. MOVED/ASK or ambiguous results must return to recovery logic; a fresh candidate is used for a new attempt. [Primary lookup](https://github.com/redis/go-redis/blob/90faf0692313160b1140f96716763227912aec44/osscluster.go#L2420-L2428).

**Observed bounded-model results:** explored three unique initializer identities, up to two metadata losses, two invalidations, and three commits of one logical Set. Replaying an initializer produced a resurrection counterexample; reacquiring generations on data-write retry produced another. The single-execution/immutable-snapshot variant explored **2,953 states / 6,477 transitions** without violating the checked invariant. This is bounded exploration, not a complete proof.

**Observed runtime experiment:** disposable Redis 7 Cluster with three primaries, all 16,384 slots healthy. A go-redis process hook injected EOF **after** an initializer had executed; the hook also drove a completed Set, invalidation, and control-hash loss before allowing the caller to see the error.

- Ordinary `ClusterClient.Eval` executed the initializer twice and restored the old incarnation. After tag-counter recreation, the old retained record matched again.
- Direct primary execution with zero node retries executed once and returned EOF.
- A new candidate installed a different incarnation; the old record was rejected, including after replaying its original data-write bytes.

The fault was injected at the process-hook boundary, not by dropping an actual network packet. Redis writes, control-hash deletion, stored-record reads, and Cluster retry handling were real. This does not verify a completed adapter, concurrent recovery stress, MOVED/ASK handling, or every supported client integration.

Evidence: `tmp/verification/issue37-recovery-model.jsonl` and `tmp/verification/issue37-recovery-cluster.jsonl` (gitignored). The cluster was stopped, all three databases were empty before teardown, and throwaway source files were removed.

### Confirmed final design

The maintainer confirmed the complete design after the experiments and final tradeoff questions. This is approval of the design and issue recording, **not a request to start implementation**.

- Keep the sharded-record/shared-control-hash shape and immutable per-Set snapshots above. Control fields do not expire independently. Ordinary generation mutations require an existing incarnation/sequence anchor and never reset it.
- Automatic recovery uses a fresh cryptographic incarnation candidate for each explicit initialization attempt and at-most-once execution of that candidate's command. Ambiguous execution does not justify reusing the candidate. Concurrent nodes adopt the existing winner.
- Require explicit primary-access/collection capabilities beyond `redis.Cmdable` where needed. Standard standalone/Cluster integrations supply these; unsupported wrappers must integrate explicitly or fail construction. Ring requires coherent control routing, not independently remapped metadata. Never silently weaken retry or primary-read guarantees.
- Change the Redis adapter's `New` to return `(*Adapter, error)` and migrate all in-repo callers. This is a deliberate source-breaking adapter change; the core L2 interfaces remain unchanged.
- Permit conditional tag-field retirement as conservative cache eviction. Reinitialization allocates a fresh sequence, so an overlapping Set may immediately miss but cannot revive an invalidated generation.
- Run application-owned, cancellable maintenance over all primaries, with repeated coverage and conditional per-record deletion. Successful continued coverage is required for eventual reclamation, including TTL-zero records. Do not close the caller's injected Redis client.
- Use logical tag invalidation and Clear; do not scan payloads on these foreground paths. Existing Redis error propagation policy still applies at the core cache surface; adapter errors must not masquerade as successful metadata mutations.
- Use a new versioned storage layout for upgrade. Automatic runtime incarnation changes invalidate the existing layout's old records without requiring an operator configuration cutover.
- Keep the accepted Redis history-rollback exclusion and shared-metadata throughput limit explicit. Keep source observations separate from inferred guarantees and bounded experimental evidence.
- Remote-only L1 invalidation, tracked separately in [issue #38](https://github.com/coefficient-engineering/cache/issues/38), now uses tag-level backplane messages and each receiver's own tag index. This is best-effort asynchronous eviction, not the L2 generation fence described here; see [Backplane and Multi-Node Caching](../explanation/BackplaneAndMultiNode.md#delivery-and-concurrent-writes) for concurrency, disconnect/recovery, and coordinated-upgrade limits.

Implementation must add production regression/integration coverage for command replay, incarnation loss/recreation, concurrent recovery, primary routing, MOVED/ASK transitions, conditional cleanup, Clear, metadata retirement, unsupported capabilities, and native finite TTL / indefinite retention. The throwaway experiments do not replace that work.

