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

## Share of connected subscription accounts

`account-share` (percent, on a key or a subscription plan; a key's value overrides its plan's) limits how much of each connected subscription account a key may use. It follows the account's own limit windows as reported by the provider: Claude's unified 5-hour and 7-day windows (`anthropic-ratelimit-unified-*` headers) and Codex's primary and secondary windows (`x-codex-*-used-percent`). It resets when that account window resets.

```yaml
keys:
  - key: "sk-laptop"
    account-share: 10     # at most 10% of each account window
  - key: "sk-phone"
    subscription: pro
    account-share: 5      # overrides the plan's share
```

How a key's portion is estimated: the provider only reports the account's total utilization, so the proxy records the API-equivalent cost (tokens for unpriced models) every key sends through each account, in 5-minute buckets. A key's portion of a window is `window utilization × key cost / total cost through that account since the window started`. Usage of the account outside this proxy (for example claude.ai) is therefore split across the keys in proportion to their use, which errs on the side of limiting early. A key is rejected with HTTP `429` (`api_key_account_share_exceeded`, `Retry-After` until the account window resets) once its portion of any active window of any account it used reaches its share. Windows appear after an account has served a request.

## Allowed models and reasoning efforts

`allowed-models` and `allowed-efforts` (on a key or a subscription plan; a key's non-empty list replaces its plan's) restrict what a key may request. An empty or missing list allows everything.

```yaml
subscriptions:
  - name: sonnet-only
    allowed-models: ["claude-sonnet-*"]
    allowed-efforts: [low, medium]
keys:
  - key: "sk-intern"
    subscription: sonnet-only
  - key: "sk-reviewer"
    subscription: sonnet-only
    allowed-efforts: [low, medium, high]   # replaces the plan's efforts, keeps its models
  - key: "sk-codex"
    allowed-models: ["gpt-5.5", "gpt-5.5-mini"]
```

- **Models** match the model name the client requests (the IDs listed by `/v1/models`), case-insensitively, after removing a thinking suffix such as `(high)` and a `models/` prefix. `*` matches any run of characters. Other models are rejected with HTTP `403` (`api_key_model_not_allowed`) and are hidden from the key's model lists (`/v1/models`, `/v1beta/models`, and the other model catalog endpoints).
- **Efforts** are the canonical levels `none`, `auto`, `minimal`, `low`, `medium`, `high`, `xhigh` and `max`. The effort is read from the request the same way the proxy reads it for usage logging: OpenAI/Codex `reasoning_effort` / `reasoning.effort`, Claude `output_config.effort` or `thinking` (a token budget is mapped to its level), Gemini `thinkingConfig`, or a model suffix like `claude-opus-5-5(max)`. A request with an effort that is not listed is rejected with HTTP `403` (`api_key_effort_not_allowed`). Requests that set no effort are accepted and use the model's default; list `none` explicitly if keys may also turn thinking off.
- An unknown value in `allowed-efforts` blocks the key with `api_key_limit_misconfigured`, like other configuration errors.
- Both lists are reported in the key's status (`allowed_models`, `allowed_efforts`) by `GET /v1/api-key/usage` and the admin usage endpoint.

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
- Admins: `GET /v8/management/observability/usage/api-key-limits` lists every configured key (masked, plus a stable `id`, the first 16 hex characters of the key's SHA-256) with its status.
- The section can be edited live with `GET`/`PUT /v8/management/config/access/api-key-limits`; saves are validated and hot-reloaded.

## Analytics and API cost

Every usage record is also aggregated per month (UTC), client key, upstream credential (`auth_index`) and model into `<auth-dir>/usage-analytics.json` (key digests only, 13 months kept, flushed every 15 seconds). For Anthropic models the record is priced at Anthropic API list prices: uncached input, cache reads and cache writes (5-minute rate, since the TTL is not reported) and output, including the Claude Haiku 5.5 long-prompt tier and the 2× fast-mode rate. Other models are counted in tokens only.

`GET /v8/management/observability/usage/analytics?month=YYYY-MM` returns the rows, key labels, and the price table with its source.

## Unraid WebUI

The `ghcr.io/<owner>/cliproxyapi-unraid` image built by `.github/workflows/unraid-image.yml` adds three things to the WebUI:

- **API Keys**: create, edit, copy and delete limited keys, and manage subscription plans, including which models (ticked from the models the proxy currently offers, or typed as patterns) and reasoning efforts each key or plan may use.
- **Overview**: each key's budget and this month's usage, plus each connected account's provider-reported subscription windows (Claude 5h/7d utilization, Codex used percent).
- **Analytics**: per-key, per-model and per-account tokens and API-equivalent cost for any recorded month, with a projected month total for the current month.
