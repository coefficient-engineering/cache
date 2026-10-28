# library features

| Feature | User outcome |
| --- | --- |
| [stampede-protection](stampede-protection.md) | Many concurrent requests for a missing key trigger one factory call and all receive its value. |
| [fail-safe](fail-safe.md) | When the factory fails, callers get the last known (stale) value instead of an error. |
| [tag-invalidation](tag-invalidation.md) | `DeleteByTag` removes every entry carrying the tag and nothing else. |
