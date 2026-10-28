# library features

| Feature | User outcome |
| --- | --- |
| [stampede-protection](stampede-protection.md) | Many concurrent requests for a missing key trigger one factory call and all receive its value. |
| [fail-safe](fail-safe.md) | When the factory fails, callers get the last known (stale) value instead of an error. |
| [tag-invalidation](tag-invalidation.md) | `DeleteByTag` removes every entry carrying the tag and nothing else. |
| [shared-factory-events](shared-factory-events.md) | The factory caller emits `Shared: false`; concurrent singleflight waiters emit `Shared: true`. |
| [skip-l2-read-when-stale](skip-l2-read-when-stale.md) | A stale L1 value can skip a fresh L2 read and invoke the factory. |
