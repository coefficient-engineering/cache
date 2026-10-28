# redis-multinode features

| Feature | User outcome |
| --- | --- |
| [shared-l2-and-invalidation](shared-l2-and-invalidation.md) | A write on one node is readable on another via L2; later updates and deletes reach the other node's L1 via the backplane. |
| [backplane-rethrow](backplane-rethrow.md) | With synchronous rethrow enabled, a failed publish is returned from `Set`/`Delete`; defaults only log it. |
