---
title: Decision
product: vercel
url: /docs/ai-gateway/modalities/decision
canonical_url: "https://vercel.com/docs/ai-gateway/modalities/decision"
last_updated: 2026-10-07
type: conceptual
prerequisites:
  - /docs/ai-gateway/modalities
  - /docs/ai-gateway
related:
  - /docs/ai-gateway/getting-started/decision
  - /docs/ai-gateway/sdks-and-apis/openai-decisions
  - /docs/ai-gateway/sdks-and-apis/typesafe
  - /docs/ai-gateway/models-and-providers/decision-fallbacks
  - /docs/ai-gateway/authentication-and-byok/byok
summary: Ask typed questions about shared state and get back structured choices, scores, and boolean probabilities through Vercel AI Gateway.
install_vercel_plugin: npx plugins add vercel/vercel-plugin
---

# Decision

Ask typed questions about a piece of shared state and get structured answers back. Decision models return choices, scores, and boolean probabilities instead of free-form text, which makes them a fit for classification, routing, rubric-based assessment, and automated verification.


<!-- docsgraph:related -->
## Related pages

> **For AI agents:** Follow these links to understand how this page connects to the rest of the Vercel ecosystem. For the full cross-link map (inbound, outbound, prerequisites, and semantic neighbors), see the .graph.md link below.

- [Decisions](https://ai-sdk.dev/docs/ai-sdk-core/decisions?from=related&source_path=%2Fdocs%2Fai-gateway%2Fmodalities%2Fdecision&source_site=vercel-docs&relationship=related) — Decide answers to Choice, Score, and Boolean questions against shared state.
- [Liquid AI's d1 is available on AI Gateway](https://vercel.com/changelog/liquid-ai-d1-is-available-on-ai-gateway?from=related&source_path=%2Fdocs%2Fai-gateway%2Fmodalities%2Fdecision&source_site=vercel-docs&relationship=related)
- [Microsoft Decision-1 now available on AI Gateway](https://vercel.com/changelog/microsoft-decision-1-now-available-on-ai-gateway?from=related&source_path=%2Fdocs%2Fai-gateway%2Fmodalities%2Fdecision&source_site=vercel-docs&relationship=related)
- [OpenAI Decisions API now available on AI Gateway](https://vercel.com/changelog/openai-decisions-api-now-available-on-ai-gateway?from=related&source_path=%2Fdocs%2Fai-gateway%2Fmodalities%2Fdecision&source_site=vercel-docs&relationship=related)
- [Automatic Model Selection](https://eve.dev/docs/guides/decide?from=related&source_path=%2Fdocs%2Fai-gateway%2Fmodalities%2Fdecision&source_site=vercel-docs&relationship=related) — Choose agent models automatically or ask typed questions in your tools and application code.
- [TypeSafe AI's Jev now available on AI Gateway](https://vercel.com/changelog/typesafe-ai-jev-now-available-on-ai-gateway?from=related&source_path=%2Fdocs%2Fai-gateway%2Fmodalities%2Fdecision&source_site=vercel-docs&relationship=related)
- [Using TanStack AI with Vercel AI Gateway](https://vercel.com/kb/guide/tanstack-ai-vercel-ai-gateway?from=related&source_path=%2Fdocs%2Fai-gateway%2Fmodalities%2Fdecision&source_site=vercel-docs&relationship=related) — Connect TanStack AI to Vercel AI Gateway with the @tanstack/ai-vercel-gateway adapter to stream chat, route across provi
- [AI Gateway now supports TypeSafe clients and an HTTP API for Jev](https://vercel.com/changelog/ai-gateway-now-supports-typesafe-clients-and-http-api-for-jev?from=related&source_path=%2Fdocs%2Fai-gateway%2Fmodalities%2Fdecision&source_site=vercel-docs&relationship=related)
- [Judge](https://eve.dev/docs/evals/judge?from=related&source_path=%2Fdocs%2Fai-gateway%2Fmodalities%2Fdecision&source_site=vercel-docs&relationship=related) — Grade evals with decision models using criteria, typed questions, or batches, and set thresholds on each assertion.
- [AI SDK with AI Gateway](https://vercel.com/docs/ai-gateway/sdks-and-apis/ai-sdk?from=related&source_path=%2Fdocs%2Fai-gateway%2Fmodalities%2Fdecision&source_site=vercel-docs&relationship=related) — Build AI-powered TypeScript applications using the AI SDK with AI Gateway for unified access to 200+ models.
- [AI Gateway](https://vercel.com/docs/agent-resources/vercel-mcp/tools/ai-gateway?from=related&source_path=%2Fdocs%2Fai-gateway%2Fmodalities%2Fdecision&source_site=vercel-docs&relationship=related) — Vercel MCP tools for ai gateway.

Full cross-link map for this page: [/docs/ai-gateway/modalities/decision.graph.md](/docs/ai-gateway/modalities/decision.graph.md?from=related&source_path=%2Fdocs%2Fai-gateway%2Fmodalities%2Fdecision&source_site=vercel-docs&relationship=graph)
<!-- /docsgraph:related -->

Several questions can be answered in parallel within a single request, against the same state.

To see which decision models AI Gateway supports, use the **Decision** filter at the [AI Gateway Models page](/ai-gateway/models?capabilities=decision).

For a step-by-step setup, see the [Decision quickstart](/docs/ai-gateway/getting-started/decision).

> **💡 Note:** Through the AI SDK, decisions require `ai` 7.0.128 or later, or
> `experimental_evaluate` on earlier AI SDK 7 releases. Decisions are also
> available through the [HTTP API](#http-api) below, the
> [OpenAI-compatible Decisions API](/docs/ai-gateway/sdks-and-apis/openai-decisions),
> and the [TypeSafe-compatible API](/docs/ai-gateway/sdks-and-apis/typesafe).
> They are not supported through Chat Completions, Responses, or the
> Anthropic-compatible or Cohere-compatible endpoints.

> **💡 Note:** Decision models were previously called evaluation models. The
> `experimental_evaluate` export and `gateway.evaluationModel()` still work as
> deprecated aliases with the same request and response shapes.

## Basic usage

```typescript filename="app/api/decide/route.ts" {5-13}
import { experimental_decide as decide } from 'ai';

export async function GET() {
  const result = await decide({
    model: 'typesafe-ai/jev',
    state: 'The support agent issued a full refund to the customer.',
    questions: {
      refunded: {
        type: 'boolean',
        instructions: 'Was a refund issued?',
      },
    },
  });

  return Response.json(result.answers);
}
```

Each key in `questions` becomes a key in `answers`:

```typescript
// result.answers
{
  refunded: { type: 'boolean', probability: 0.99 }
}
```

## Question types

### Boolean

Returns a probability between 0 and 1. Supply `criteria` to define what the true and false cases mean.

```typescript
const result = await decide({
  model: 'typesafe-ai/jev',
  state: 'The build failed with exit code 1.',
  questions: {
    passed: {
      type: 'boolean',
      instructions: 'Did the build succeed?',
      criteria: {
        true: 'exit code 0',
        false: 'any non-zero exit code',
      },
    },
  },
});

// { passed: { type: 'boolean', probability: 0.01 } }
```

### Choice

Picks one option from a named set. `criteria` is a record of option names to descriptions, and the answer carries both the selected `choice` and the probability of each option.

```typescript
const result = await decide({
  model: 'typesafe-ai/jev',
  state: 'My card was charged twice for one order.',
  questions: {
    route: {
      type: 'choice',
      instructions: 'Route this support ticket.',
      criteria: {
        billing: 'payment or charge problems',
        shipping: 'delivery problems',
        technical: 'application bugs',
      },
    },
  },
});

// {
//   route: {
//     type: 'choice',
//     choice: 'billing',
//     probabilities: { billing: 1, shipping: 0, technical: 0 },
//   },
// }
```

### Score

Rates the state along an ordered scale. `criteria` is an array of at least two labels, ordered lowest to highest. The answer is an interpolated `score` plus the probability of each rung.

```typescript
const result = await decide({
  model: 'typesafe-ai/jev',
  state: 'The PR adds tests, updates docs, and has a clear description.',
  questions: {
    quality: {
      type: 'score',
      instructions: 'Rate the quality of this pull request.',
      criteria: [
        'poor: no tests or docs',
        'fair: partial coverage',
        'good: tests and docs',
        'excellent: tests, docs, and clear rationale',
      ],
    },
  },
});

// {
//   quality: {
//     type: 'score',
//     score: 2.97,
//     probabilities: { '0': 0, '1': 0, '2': 0.02, '3': 0.98 },
//   },
// }
```

## Multiple questions in one request

Questions of different types can share a single state, and are answered in one round trip.

```typescript filename="app/api/triage/route.ts" {7-24}
import { experimental_decide as decide } from 'ai';

export async function GET() {
  const result = await decide({
    model: 'typesafe-ai/jev',
    state: 'I cannot log in, and I also want a refund for last month.',
    questions: {
      authIssue: {
        type: 'boolean',
        instructions: 'Is there a login problem?',
      },
      wantsRefund: {
        type: 'boolean',
        instructions: 'Is a refund requested?',
      },
      urgency: {
        type: 'score',
        instructions: 'How urgent is this ticket?',
        criteria: ['low', 'medium', 'high'],
      },
    },
  });

  return Response.json(result.answers);
}
```

## Escalating uncertain decisions

Decision fallbacks can rerun a successful decision with another model when a Choice or Score has low confidence, a Boolean probability falls inside an uncertain band, or a compound condition matches. Configure the policy under `providerOptions.gateway.models` for AI SDK `decide`, `POST /v1/evaluate`, or the TypeSafe-compatible API.

Decision fallbacks are opt-in and support one successful-response fallback stage. See [Decision Fallbacks](/docs/ai-gateway/models-and-providers/decision-fallbacks) for the condition contract, full-request rerun behavior, response metadata, and two-stage billing.

## Structured state

`state` accepts a string, an object, or an array, so you can pass structured records or a message history directly without serializing them yourself.

```typescript
const result = await decide({
  model: 'typesafe-ai/jev',
  state: {
    order: { id: 'A-1', total: 42.5, status: 'refunded' },
    agent: 'bot-7',
  },
  questions: {
    refunded: {
      type: 'boolean',
      instructions: 'Is the order refunded?',
    },
  },
});
```

## AI Gateway provider instance

When using an AI Gateway provider instance, specify decision models with `gateway.decisionModel(...)`.

```typescript filename="app/api/decide/route.ts" {2,6}
import { experimental_decide as decide } from 'ai';
import { gateway } from '@ai-sdk/gateway';

export async function GET() {
  const result = await decide({
    model: gateway.decisionModel('typesafe-ai/jev'),
    state: 'The support agent issued a full refund to the customer.',
    questions: {
      refunded: {
        type: 'boolean',
        instructions: 'Was a refund issued?',
      },
    },
  });

  return Response.json(result.answers);
}
```

## HTTP API

If you are not using the AI SDK, post to `/v1/evaluate` with the same `model`, `state`, and `questions` fields.

```bash filename="decide.sh"
curl https://ai-gateway.vercel.sh/v1/evaluate \
  -H "Authorization: Bearer $AI_GATEWAY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "typesafe-ai/jev",
    "state": "I was charged twice for my subscription.",
    "questions": {
      "refund": {
        "type": "boolean",
        "instructions": "Is the customer asking for money back?"
      }
    }
  }'
```

The response reports the model that ran, the answers, token usage, and AI Gateway routing and cost metadata:

```json
{
  "model": "typesafe-ai/jev",
  "answers": { "refund": { "type": "boolean", "probability": 0.98 } },
  "usage": { "inputTokens": 275, "outputTokens": 20 },
  "providerMetadata": {
    "gateway": {
      "routing": {
        "originalModelId": "typesafe-ai/jev",
        "resolvedProvider": "typesafe-ai",
        "canonicalSlug": "typesafe-ai/jev",
        "finalProvider": "typesafe-ai"
      },
      "cost": "0.00001155",
      "marketCost": "0.00001155",
      "surchargeCost": "0",
      "gatewayCost": "0.00001155",
      "generationId": "gen_..."
    }
  }
}
```

Decision requests work with [BYOK](/docs/ai-gateway/authentication-and-byok/byok). If your team has added a key for the provider, it is used automatically.

### Provider options

`/v1/evaluate` accepts the same `providerOptions` as other AI Gateway endpoints, so you can require zero data retention or restrict which providers may serve the request:

```json
{
  "model": "typesafe-ai/jev",
  "state": "...",
  "questions": { "refund": { "type": "boolean", "instructions": "..." } },
  "providerOptions": {
    "gateway": { "zeroDataRetention": true, "only": ["typesafe-ai"] }
  }
}
```

String entries in `providerOptions.gateway.models` can name native decision models or language models, and a language model answers through structured output. The array also accepts one conditional decision fallback object. Provider-specific namespaces, such as `providerOptions.openai`, carry to a matching fallback provider. See [Decision Fallbacks](/docs/ai-gateway/models-and-providers/decision-fallbacks#configure-a-choice-or-score-confidence-fallback) for complete requests.

## OpenAI-compatible API

If you use OpenAI's Decisions API, point the OpenAI SDK at `https://ai-gateway.vercel.sh/v1` and call `decisions.create` as before. AI Gateway accepts OpenAI's `predicate`, `choice`, and `score` questions and returns OpenAI's response shape, with any decision model answering.

See [OpenAI-compatible Decisions API](/docs/ai-gateway/sdks-and-apis/openai-decisions) for the request and response formats.

## TypeSafe-compatible API

If you already call TypeSafe with its SDK, you don't need to rewrite your requests. Point the client at `https://ai-gateway.vercel.sh/typesafe` and AI Gateway accepts TypeSafe's own request and response shapes, including `noul` for Boolean questions. Decision fallbacks work there too.

See [TypeSafe API](/docs/ai-gateway/sdks-and-apis/typesafe) for authentication, supported endpoints, and response headers.

## Usage and pricing

Decision requests report token usage like any other model, and are billed from the model's per-token rates. Check the [AI Gateway Models page](/ai-gateway/models?capabilities=decision) for the rates on a specific model, since some decision models price input tokens only.

When a decision fallback triggers, both stages are billed and the response usage totals both calls. The stages run sequentially, so the request also includes both model calls' latency. See [two-stage usage and latency](/docs/ai-gateway/models-and-providers/decision-fallbacks#account-for-two-stage-usage-and-latency) for details.

```typescript
const result = await decide({
  model: 'typesafe-ai/jev',
  state: 'The support agent issued a full refund.',
  questions: {
    refunded: { type: 'boolean', instructions: 'Was a refund issued?' },
  },
});

console.log(result.usage);
// { inputTokens: 283, outputTokens: 21 }
```

## Next steps

- Explore [Jev guides and use cases](/kb/jev-from-typesafe-ai)
- Learn how to [classify, route, and score with Jev and AI SDK](/kb/guide/typesafe-jev-and-ai-sdk) and [route form submissions](/kb/guide/jev-ai-sdk-form-router)
- Learn how to [automatically approve tool calls in eve with Jev](/kb/guide/auto-approve-tool-calls-eve-jev)
- Build a [product review moderation workflow with Jev and TanStack AI](/kb/guide/moderate-product-reviews-jev-tanstack-ai)


---

[View full sitemap](/docs/sitemap)
