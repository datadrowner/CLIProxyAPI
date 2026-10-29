# Per-key token limits

`access.api-key-limits` lets you hand out several client API keys with different budgets:

| Mode | Configuration | Behavior |
| --- | --- | --- |
| Full usage | `key` only | Unlimited; usage is still counted and reported. |
| Fixed token budget | `token-limit` (+ optional `period`) | Blocked once the budget is used up. |
| Subscription | `subscription: <name>` | Uses the token limit and period of a named plan. A key-level `token-limit` or `period` overrides the plan. |

Keys listed under `api-key-limits.keys` are accepted for authentication automatically; they do not need to be repeated in `access.api-keys`. Keys only in `access.api-keys` keep their existing, unlimited behavior.

```yaml
access:
  api-keys:
    - "legacy-key"
  api-key-limits:
    subscriptions:
      - name: starter
        token-limit: 1000000
        period: monthly
      - name: pro
        token-limit: 2000000
        period: daily
    keys:
      - key: "sk-admin"
        name: admin              # full usage
      - key: "sk-trial"
        name: trial
        token-limit: 50000        # 50k tokens total, never resets
      - key: "sk-alice"
        name: alice
        subscription: pro
      - key: "sk-bob"
        name: bob
        subscription: starter
        period: weekly            # starter budget, but weekly
```

## Periods

- `total` (default): the budget never resets.
- `daily`, `weekly` (weeks start Monday), `monthly`: calendar periods in UTC.
- A Go duration such as `5h` or `168h`: a fixed window that starts on first use and restarts once it has elapsed.

## Counting and enforcement

- Tokens are counted from every usage record the proxy emits for the key (`total_tokens`, or input + output when no total is reported). Requests whose upstream reports no usage are not charged.
- The budget is checked when a request starts. A request that starts under the budget is allowed to finish, so the final request can overshoot the limit.
- Exhausted keys receive HTTP `429` with `code: "api_key_token_limit_exceeded"` and a `Retry-After` header when the period resets. Long-lived `/v1/responses` WebSocket sessions are checked on every turn.
- A key that references an unknown subscription or has an invalid period is rejected with HTTP `403` (`api_key_limit_misconfigured`) instead of being treated as unlimited.
- Limited keys get `X-RateLimit-Limit-Tokens`, `X-RateLimit-Remaining-Tokens` and, for periodic budgets, `X-RateLimit-Reset-Tokens` response headers.

## State

Counters are written to `state-file` (default `<auth-dir>/api-key-usage.json`) after every usage record, so budgets survive restarts. The file stores SHA-256 digests of keys, never the keys themselves. Delete an entry, or the whole file while the server is stopped, to reset budgets. Changing a key's `period` starts a new window; changing its `token-limit` keeps the tokens already used. Config hot-reload applies changes without a restart.

## Inspecting usage

- Clients: `GET /v1/api-key/usage` with their own key returns their limit, used and remaining tokens, and reset time. It still works after the budget is exhausted.
- Admins: `GET /v8/management/observability/usage/api-key-limits` lists every configured key (masked) with its status.
