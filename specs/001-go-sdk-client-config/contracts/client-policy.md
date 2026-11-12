# Existing Client Configuration Contract

No public signature or struct field changes. Both constructors have the same behavior.

| Setting | Behavior |
|---------|----------|
| Zero timeout | No default deadline |
| Positive timeout | Unary default only when caller has no deadline; one budget across retries |
| Explicit deadline | Authoritative whether shorter or longer |
| Nil retry policy or zero retry count | No SDK-configured additional attempts |
| Positive retry count | At most that many additional attempts for eligible reads on original gRPC Unavailable |
| Zero waits | Default initial 100 ms / maximum 5 s |
| Backoff | Initial, twice initial, and so on; cap safely at maximum |
| Negative settings or effective maximum below initial | Constructor returns SDK invalid-argument error |
| Nil logger | No configured logger output |
| Logger | Logical unary completion/retry metadata with no application or credential data |

Streams and sessions use caller contexts for lifetime. Unary setup calls follow unary defaults. Polling helpers have no implicit overall timeout. Caller-facing detailed error conversion is unchanged; sanitization applies to logs.

Service-config retries are disabled so they cannot bypass the closed safe-read list. Pre-application gRPC transparent retries remain transport behavior. No stream retry/reconnect is introduced.
