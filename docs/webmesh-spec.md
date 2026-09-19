# agent.webmesh.ai — Complete Endpoint Specification

> Reference for building identical agent endpoints. All data captured live on 2026-09-19.

---

## 1. Site Map

The site is a **single HTML page** at `https://agent.webmesh.ai/` with no navigation, footer, or subpages. No interactive chat, demo form, or query interface exists. The page is a plain-HTML reference card listing skills, interoperability mechanisms, and links to every endpoint.

### All URLs served by agent.webmesh.ai (16 live endpoints + 1 org-level)

| Path | Content-Type | Status | Purpose |
|------|-------------|--------|---------|
| `/` | `text/html` | 200 | Human-readable agent info page |
| `/.well-known/agent-card.json` | `application/json` | 200 | A2A Agent Card (core identity) |
| `/.well-known/ans/trust-card.json` | `application/json` | 200 | ANS Trust Card (keys + x5c + SCITT receipt) |
| `/.well-known/mcp.json` | `application/json` | 200 | MCP server discovery card |
| `/.well-known/did.json` | `application/json` | 200 | W3C DID document (did:web) |
| `/.well-known/ard.json` | `application/json` | 200 | Agent Resource Discovery (AI-Catalog) |
| `/.well-known/ai-catalog.json` | `application/json` | 200 | Identical to ard.json (alias) |
| `/.well-known/jwks.json` | `application/json` | 200 | JWKS (Ed25519 public key) |
| `/.well-known/http-message-signatures-directory` | `application/json` | 200 | Web Bot Auth key directory (same JSON as trust card) |
| `/.well-known/signature-agent-card` | `application/json` | 200 | Signed agent card (same JSON as trust card) |
| `/.well-known/dnsid/status.json` | `application/json` | 200 | DNSid status endpoint |
| `/agentfacts.json` | `application/json` | 200 | NANDA AgentFacts profile |
| `/health` | `application/json` | 200 | Health check |
| `/mcp` | (streamable-http) | 406 on GET | MCP endpoint (JSON-RPC over streamable-HTTP, POST only) |
| `/llms.txt` | `text/plain` | 200 | LLM context file |
| `/robots.txt` | `text/plain` | 200 | Crawler rules + Agentmap reference |

### URLs that return 404

| Path | Notes |
|------|-------|
| `/catalog.json` | Not served; the catalog is at `/.well-known/ai-catalog.json` |
| `/openapi.json` | Not served |
| `/.well-known/ai-plugin.json` | Not served (ChatGPT plugin format not used) |

---

## 2. Endpoint Responses (Full JSON)

### 2.1 `/.well-known/agent-card.json` — A2A Agent Card

```json
{
  "name": "Webmesh Agent",
  "url": "https://agent.webmesh.ai",
  "description": "Interrogates other agents by hostname. Verifies whether an agent is what it claims, from its DNS records, DNSSEC signatures, Agent Name Service (ANS) Transparency Log proof, and published agent card. Sends any agent a live A2A message and reports the credential it requires. Searches the ANS registry for agents by capability or protocol. The verify and interact checks run against any agent, whether or not it is ANS-registered; the registry search covers agents registered in ANS.",
  "version": "1.0.13",
  "protocolVersion": "1.0",
  "provider": {
    "organization": "Webmesh",
    "url": "https://webmesh.ai",
    "did": "did:web:agent.webmesh.ai"
  },
  "documentationUrl": "https://agent.webmesh.ai",
  "supportedInterfaces": [
    {
      "url": "https://agent.webmesh.ai",
      "protocolBinding": "jsonrpc",
      "protocolVersion": "1.0"
    }
  ],
  "capabilities": {
    "streaming": false,
    "pushNotifications": false,
    "extendedAgentCard": false,
    "extensions": [
      {
        "uri": "https://modelcontextprotocol.io",
        "description": "MCP server exposing read-only verification tools over streamable-HTTP.",
        "required": false,
        "params": {
          "endpoint": "https://agent.webmesh.ai/mcp",
          "transport": "streamable-http",
          "protocolVersion": "2025-03-26",
          "discoveryUrl": "https://agent.webmesh.ai/.well-known/mcp.json"
        }
      },
      {
        "uri": "https://webmesh.ai/ext/ans-trust-stack/v1",
        "description": "Identity and interoperability stack: ANS Trust Card (x5c chain + stapled SCITT receipt), DNS-AID SVCB with DNSSEC and DANE TLSA, DNSid organizational accountability, ARD / AI-Catalog discovery, and Web Bot Auth (RFC 9421 HTTP Message Signatures) outbound request signing.",
        "required": false,
        "params": {
          "trustCard": "https://agent.webmesh.ai/.well-known/ans/trust-card.json",
          "ard": "https://agent.webmesh.ai/.well-known/ard.json",
          "agentFacts": "https://agent.webmesh.ai/agentfacts.json",
          "httpMessageSignaturesDirectory": "https://agent.webmesh.ai/.well-known/http-message-signatures-directory",
          "identityAnchors": [
            "ans-x509", "did:web", "dns-aid", "dnssec", "dane-tlsa", "dnsid"
          ],
          "outboundSigning": "web-bot-auth"
        }
      }
    ]
  },
  "securitySchemes": {
    "noAuth": {
      "type": "noAuth",
      "description": "This agent is publicly accessible with no authentication required. All skills are available to any caller."
    },
    "ansIdentityCert": {
      "type": "mutualTLS",
      "description": "ANS Identity Certificate issued by the ANS Registration Authority. The agent presents this cert during the TLS handshake; clients verify against the chain advertised in the Trust Card's keys[].x5c. See https://agent.webmesh.ai/.well-known/ans/trust-card.json"
    },
    "httpMessageSignatures": {
      "type": "http",
      "scheme": "signature",
      "description": "RFC 9421 HTTP Message Signatures over response components, using the Ed25519 key advertised in the Trust Card. Public key directory at https://agent.webmesh.ai/.well-known/http-message-signatures-directory"
    }
  },
  "securityRequirements": [
    { "noAuth": [] }
  ],
  "defaultInputModes": ["text/plain", "application/json"],
  "defaultOutputModes": ["application/json", "text/plain"],
  "skills": [
    {
      "id": "verify",
      "name": "Agent Verification",
      "description": "Checks identity and compatibility for an agent at a given FQDN. Returns a four-dimension compatibility verdict (identity / protocol / auth / attestations, each pass/warning/unable-to-check) and a can_traveler_transact summary, plus: A2A protocol version vs our v1.0 baseline, all endpoints (A2A and MCP) with versions, bearer auth forms and mandate authority, SPIFFE ID if present, and the full identity evidence chain (ANS TL proof, DNSSEC, DNS-AID, DNSid). Works for any agent, ANS-registered or not.",
      "tags": ["verification", "trust", "ANS", "DNS-AID", "DNSSEC", "interop"],
      "examples": [
        "Verify agent.webmesh.ai",
        "Is travel.agenthaven.dev compatible with our traveler?",
        "Check the evidence trail for ans://v1.0.2.agent.webmesh.ai",
        "What protocol does bookings.example.com speak?"
      ],
      "inputModes": ["text/plain", "application/json"],
      "outputModes": ["application/json", "text/plain"],
      "securityRequirements": [{ "noAuth": [] }]
    },
    {
      "id": "discover",
      "name": "Agent Discovery",
      "description": "Searches the ANS registry for agents matching a free-text query, a capability description, or a protocol filter. Returns Trust Index-scored candidates. Use this to find agents before verifying or interacting with them.",
      "tags": ["discovery", "search", "ANS"],
      "examples": [
        "Find agents that handle DNS diagnostics",
        "List ANS-registered A2A agents tagged with compliance",
        "Which agents in prod cover IP address management?",
        "Show me MCP agents for infrastructure operations"
      ],
      "inputModes": ["text/plain", "application/json"],
      "outputModes": ["application/json", "text/plain"],
      "securityRequirements": [{ "noAuth": [] }]
    },
    {
      "id": "interact",
      "name": "Agent Interaction",
      "description": "Sends a message to another A2A agent and returns its reply. Reads the target agent card to determine the correct endpoint and authentication requirements. For agents that require OAuth2, reports exactly what credential is needed and where to get it. For open agents, sends the message and returns the response directly.",
      "tags": ["interaction", "A2A", "interconnect"],
      "examples": [
        "Ask ddi-agent.ai.infoblox.com what DNS skills it has",
        "Talk to agent.example.com: what can you do?",
        "Send 'list your skills' to the agent at bookings.example.com"
      ],
      "inputModes": ["text/plain", "application/json"],
      "outputModes": ["application/json", "text/plain"],
      "securityRequirements": [{ "noAuth": [] }]
    }
  ],
  "x-identity": {
    "ans": {
      "uri": "ans://v1.0.13.agent.webmesh.ai",
      "trustCard": "https://agent.webmesh.ai/.well-known/ans/trust-card.json",
      "transparencyLog": "https://transparency.ans.godaddy.com/v1/agents/de02d013-138e-4e50-9cae-daa20bc3dc37"
    },
    "wimse": {
      "spiffeId": "spiffe://webmesh.ai/agents/agent",
      "jwksUri": "https://agent.webmesh.ai/.well-known/jwks.json",
      "supportedProfiles": ["urn:ietf:params:wimse:agent-delegation-chain"],
      "signingAlgs": ["EdDSA"]
    }
  },
  "x-discovery": {
    "ans_registered": "prod",
    "ans_name": "ans://v1.0.13.agent.webmesh.ai",
    "tl_badge": "https://transparency.ans.godaddy.com/v1/agents/de02d013-138e-4e50-9cae-daa20bc3dc37",
    "trust_index": {
      "score_url": "https://api.godaddy.com/v1/ans/registered-agents?query=agent.webmesh.ai",
      "score_field": "scores.trustScore",
      "auth": "sso-key"
    },
    "dns_aid_svcb": "agent.webmesh.ai IN SVCB 1 . alpn=a2a,h2"
  },
  "x-security-note": "This agent is publicly accessible with no authentication required (noAuth). The ansIdentityCert scheme (mutual TLS, ANS private CA) is declared for future ANS-to-ANS production calls but is not currently enforced. The card accurately describes what is enforced.",
  "signatures": [
    {
      "protected": "eyJhbGciOiJFZERTQSIsImprdSI6Imh0dHBzOi8vYWdlbnQud2VibWVzaC5haS8ud2VsbC1rbm93bi9hbnMvdHJ1c3QtY2FyZC5qc29uIiwia2lkIjoib2NtSldqeVZEdU1VaXlaTWE2cE9Hcmd4X2RaaFNuU0RzejM1aG1OLWs5ayIsInR5cCI6ImFnZW50LWNhcmQrandzIn0",
      "signature": "RLXWOC6V2CQnwMd45OrEffbcJwXHJHuBc3ix9jvevre2-ThsxON18m25CVXn2WqjSbblIMkhS2SL_3cwYqE4DA",
      "header": {
        "kid": "ocmJWjyVDuMUiyZMa6pOGrgx_dZhSnSDsz35hmN-k9k"
      }
    }
  ]
}
```

**Signature `protected` header decoded:**
```json
{
  "alg": "EdDSA",
  "jku": "https://agent.webmesh.ai/.well-known/ans/trust-card.json",
  "kid": "ocmJWjyVDuMUiyZMa6pOGrgx_dZhSnSDsz35hmN-k9k",
  "typ": "agent-card+jws"
}
```

---

### 2.2 `/.well-known/ans/trust-card.json` — ANS Trust Card

```json
{
  "ansName": "ans://v1.0.13.agent.webmesh.ai",
  "agentDisplayName": "agent.webmesh.ai",
  "version": "1.0.13",
  "agentHost": "agent.webmesh.ai",
  "endpoints": [
    {
      "protocol": "A2A",
      "agentUrl": "https://agent.webmesh.ai",
      "metaDataUrl": "https://agent.webmesh.ai/.well-known/agent-card.json"
    }
  ],
  "keys": [
    {
      "kty": "OKP",
      "crv": "Ed25519",
      "x": "ihnMrBbo3WFd7wCOKBkcOr73CrAgkGIEaAQryO0OtS8",
      "use": "sig",
      "kid": "ocmJWjyVDuMUiyZMa6pOGrgx_dZhSnSDsz35hmN-k9k",
      "x5c": [
        "<base64-encoded X.509 certificate issued by GoDaddy Private ANS Issuing CA - PR1v1>"
      ]
    }
  ],
  "agentId": "de02d013-138e-4e50-9cae-daa20bc3dc37",
  "transparencyReceipt": "<CBOR-encoded SCITT receipt (base64)>",
  "botProfile": {
    "client_name": "agent.webmesh.ai",
    "client_uri": "https://agent.webmesh.ai",
    "expected-user-agent": "webmesh-agent/1.0 (+https://agent.webmesh.ai)",
    "trigger": "fetcher",
    "purpose": "Interrogates other agents by hostname..."
  }
}
```

**Key fields explained:**

| Field | Type | Description |
|-------|------|-------------|
| `ansName` | string | Versioned ANS URI: `ans://<version>.<host>` |
| `agentDisplayName` | string | Human-readable hostname |
| `version` | string | Agent version matching the agent card |
| `agentHost` | string | FQDN of the agent |
| `endpoints[]` | array | Protocol endpoints with metadata URLs |
| `keys[]` | array | JWK keys with x5c certificate chains |
| `keys[].kty` | string | Key type: `"OKP"` for Ed25519 |
| `keys[].crv` | string | Curve: `"Ed25519"` |
| `keys[].x` | string | Base64url-encoded public key |
| `keys[].use` | string | `"sig"` (signature) |
| `keys[].kid` | string | Key ID (thumbprint) |
| `keys[].x5c` | string[] | X.509 cert chain (DER, base64) |
| `agentId` | string (UUID) | ANS registry UUID |
| `transparencyReceipt` | string | CBOR SCITT receipt (base64) |
| `botProfile` | object | Web Bot Auth profile (RFC draft) |

---

### 2.3 `/.well-known/mcp.json` — MCP Discovery Card

```json
{
  "mcpVersion": "2025-03-26",
  "version": "1.0.13",
  "endpoint": "https://agent.webmesh.ai/mcp",
  "transport": "streamable-http",
  "authentication": {
    "schemes": ["none"],
    "notes": "No HTTP authentication required. Tools exposed: verify, discover, interact. Outbound A2A calls are not exposed over this MCP endpoint."
  },
  "tools": [
    {
      "name": "verify",
      "description": "Checks identity and compatibility for an agent at a given FQDN..."
    },
    {
      "name": "discover",
      "description": "Searches the ANS registry for agents matching a free-text query..."
    },
    {
      "name": "interact",
      "description": "Sends a message to another A2A agent and returns its reply..."
    }
  ],
  "agentCardUrl": "https://agent.webmesh.ai/.well-known/agent-card.json",
  "trustCardUrl": "https://agent.webmesh.ai/.well-known/ans/trust-card.json"
}
```

---

### 2.4 `/.well-known/did.json` — W3C DID Document

```json
{
  "@context": [
    "https://www.w3.org/ns/did/v1",
    "https://w3id.org/security/suites/jws-2020/v1"
  ],
  "id": "did:web:agent.webmesh.ai",
  "verificationMethod": [
    {
      "id": "did:web:agent.webmesh.ai#ans-key",
      "type": "JsonWebKey2020",
      "controller": "did:web:agent.webmesh.ai",
      "publicKeyJwk": {
        "kty": "OKP",
        "crv": "Ed25519",
        "x": "ihnMrBbo3WFd7wCOKBkcOr73CrAgkGIEaAQryO0OtS8",
        "kid": "ocmJWjyVDuMUiyZMa6pOGrgx_dZhSnSDsz35hmN-k9k",
        "use": "sig"
      }
    }
  ],
  "authentication": ["did:web:agent.webmesh.ai#ans-key"],
  "assertionMethod": ["did:web:agent.webmesh.ai#ans-key"],
  "service": [
    {
      "id": "did:web:agent.webmesh.ai#a2a",
      "type": "AgentService",
      "serviceEndpoint": "https://agent.webmesh.ai/"
    },
    {
      "id": "did:web:agent.webmesh.ai#mcp",
      "type": "MCPService",
      "serviceEndpoint": "https://agent.webmesh.ai/mcp"
    }
  ]
}
```

---

### 2.5 `/agentfacts.json` — NANDA AgentFacts

```json
{
  "agent_name": "webmesh-agent",
  "label": "webmesh-agent",
  "description": "ANS-registered reference agent at agent.webmesh.ai. Speaks A2A 1.0 (JSON-RPC, backward-compatible with 0.3) and MCP (streamable HTTP) at one origin. Serves a hybrid Trust Card with a stapled SCITT receipt that verifies offline against the GoDaddy ANS Transparency Log. Signs its own outbound requests per Web Bot Auth (RFC 9421 HTTP Message Signatures) so edge networks recognise it as a verified bot. Working proof for the IETF Agent Name Service draft.",
  "version": "1.0.13",
  "documentationUrl": "https://github.com/godaddy/ans",
  "jurisdiction": "USA",
  "provider": {
    "name": "webmesh.ai",
    "url": "https://agent.webmesh.ai",
    "did": "did:web:agent.webmesh.ai"
  },
  "endpoints": {
    "static": [
      "https://agent.webmesh.ai/",
      "https://agent.webmesh.ai/mcp"
    ],
    "adaptive_resolver": {
      "url": "https://agent.webmesh.ai/",
      "policies": [
        "http-message-signatures",
        "web-bot-auth",
        "ans-trust-card",
        "ard-v1",
        "dns-aid",
        "dnsid"
      ]
    }
  },
  "capabilities": {
    "modalities": ["text"],
    "streaming": false,
    "batch": false,
    "authentication": {
      "methods": ["http-message-signatures"],
      "requiredScopes": []
    }
  },
  "skills": [
    {
      "id": "verify",
      "description": "...",
      "inputModes": ["text/plain", "application/json"],
      "outputModes": ["application/json", "text/plain"],
      "supportedLanguages": ["en"],
      "latencyBudgetMs": 5000,
      "maxTokens": 8192
    },
    {
      "id": "discover",
      "description": "...",
      "inputModes": ["text/plain", "application/json"],
      "outputModes": ["application/json", "text/plain"],
      "supportedLanguages": ["en"],
      "latencyBudgetMs": 5000,
      "maxTokens": 8192
    },
    {
      "id": "interact",
      "description": "...",
      "inputModes": ["text/plain", "application/json"],
      "outputModes": ["application/json", "text/plain"],
      "supportedLanguages": ["en"],
      "latencyBudgetMs": 5000,
      "maxTokens": 8192
    }
  ],
  "telemetry": {
    "enabled": false,
    "retention": "7d",
    "sampling": 0.1
  },
  "certification": {
    "level": "verified",
    "issuer": "NANDA",
    "issuanceDate": "2026-05-18",
    "expirationDate": "2027-05-18"
  }
}
```

---

### 2.6 `/health`

```json
{
  "status": "ok",
  "a2aProtocolVersion": "1.0",
  "mcpProtocolVersion": "2025-03-26"
}
```

---

### 2.7 `/.well-known/jwks.json` — JSON Web Key Set

```json
{
  "keys": [
    {
      "kty": "OKP",
      "crv": "Ed25519",
      "x": "ihnMrBbo3WFd7wCOKBkcOr73CrAgkGIEaAQryO0OtS8",
      "use": "sig",
      "kid": "ocmJWjyVDuMUiyZMa6pOGrgx_dZhSnSDsz35hmN-k9k"
    }
  ]
}
```

---

### 2.8 `/robots.txt`

```
User-agent: *
Allow: /
Agentmap: https://agent.webmesh.ai/.well-known/ai-catalog.json
```

---

### 2.9 `/.well-known/ard.json` / `/.well-known/ai-catalog.json` — Agent Resource Discovery

These two URLs serve identical content. Structure:

```json
{
  "specVersion": "1.0",
  "host": {
    "displayName": "Webmesh",
    "identifier": "https://agent.webmesh.ai",
    "documentationUrl": "https://agent.webmesh.ai",
    "trustManifest": {
      "identity": "https://agent.webmesh.ai/.well-known/ans/trust-card.json",
      "trustSchema": {
        "identifier": "urn:trust:ans-trust-card-v1",
        "version": "1.0",
        "governanceUri": "https://github.com/godaddy/ans",
        "verificationMethods": ["x509", "jwk"]
      },
      "attestations": [
        {
          "type": "publisher-identity",
          "uri": "https://agent.webmesh.ai/.well-known/ans/trust-card.json",
          "mediaType": "application/json",
          "description": "ANS Trust Card..."
        },
        {
          "type": "a2a-agent-card",
          "uri": "https://agent.webmesh.ai/.well-known/agent-card.json",
          "mediaType": "application/a2a-agent-card+json",
          "description": "A2A v0.3 AgentCard with detached-JWS signatures..."
        }
      ],
      "provenance": [
        {
          "relation": "registeredWith",
          "sourceId": "https://github.com/gdcorp-domains/ans-registry-poc",
          "registryUri": "https://api.godaddy.com/v1/agents"
        }
      ],
      "subject": {
        "type": "application/json",
        "url": "https://agent.webmesh.ai/.well-known/ans/trust-card.json",
        "digest": "sha256:<hex>"
      },
      "issuedAt": "<ISO 8601>",
      "signature": "<EdDSA JWS compact>"
    }
  },
  "entries": [
    {
      "identifier": "urn:air:agent.webmesh.ai:webmesh-agent:a2a",
      "type": "application/a2a-agent-card+json",
      "displayName": "Webmesh Agent (A2A)",
      "version": "1.0.13",
      "url": "https://agent.webmesh.ai/.well-known/agent-card.json",
      "description": "...",
      "tags": ["A2A", "ANS", "DNS-AID", "DNSSEC", "discovery", "interaction", "interconnect", "interop", "search", "trust", "verification", "a2a"],
      "publisher": {
        "identifier": "https://agent.webmesh.ai/.well-known/ans/trust-card.json",
        "displayName": "Webmesh",
        "identityType": "https"
      },
      "trustManifest": { "..." : "per-entry trust manifest" },
      "updatedAt": "<ISO 8601>",
      "representativeQueries": [
        "Verify agent.webmesh.ai",
        "Is travel.agenthaven.dev compatible with our traveler?",
        "Find agents that handle DNS diagnostics",
        "List ANS-registered A2A agents tagged with compliance",
        "Ask ddi-agent.ai.infoblox.com what DNS skills it has"
      ]
    },
    {
      "identifier": "urn:air:agent.webmesh.ai:webmesh-agent:mcp",
      "type": "application/mcp-server-card+json",
      "displayName": "Webmesh Agent (MCP)",
      "version": "1.0.13",
      "url": "https://agent.webmesh.ai/.well-known/mcp.json",
      "description": "MCP server exposing read-only verification tools over streamable-HTTP.",
      "tags": ["...", "mcp"],
      "...": "same structure as A2A entry"
    },
    {
      "identifier": "urn:air:agent.webmesh.ai:webmesh-agent:nanda",
      "type": "application/json",
      "displayName": "Webmesh Agent (NANDA AgentFacts)",
      "version": "1.0.13",
      "url": "https://agent.webmesh.ai/agentfacts.json",
      "description": "Project NANDA AgentFacts: operational profile, adaptive-resolver policies, and governance metadata.",
      "tags": ["...", "nanda", "agentfacts"],
      "...": "same structure as A2A entry"
    }
  ],
  "extensions": {
    "ansName": "ans://v1.0.13.agent.webmesh.ai",
    "trustCardUrl": "https://agent.webmesh.ai/.well-known/ans/trust-card.json"
  }
}
```

---

### 2.10 `/.well-known/signature-agent-card` — Signed Agent Card

Serves **identical JSON** to `/.well-known/ans/trust-card.json`. This is the endpoint referenced in the agent card's detached JWS `jku` header. Same structure: `ansName`, `agentDisplayName`, `version`, `agentHost`, `endpoints[]`, `keys[]` (with `x5c`), `agentId`, `transparencyReceipt`, `botProfile`.

---

### 2.11 `/.well-known/http-message-signatures-directory` — Web Bot Auth Directory

Serves **identical JSON** to `/.well-known/ans/trust-card.json`. This is the public-key directory for RFC 9421 HTTP Message Signatures verification. Clients fetch this to get the Ed25519 key (from `keys[]`) to verify outbound request signatures.

---

### 2.12 `/.well-known/dnsid/status.json` — DNSid Status

```json
{
  "status": "ACTIVE"
}
```

Referenced by the `su=` parameter in the `_dnsid` TXT record. Confirms the DNSid identity assertion is active.

---

### 2.13 `webmesh.ai/.well-known/dnsid/entity-keys.json` — Org-Level JWKS

> **Note:** This is served from `webmesh.ai`, NOT `agent.webmesh.ai`. It is the **organization-level** key, distinct from the agent's key.

```json
{
  "keys": [
    {
      "alg": "EdDSA",
      "crv": "Ed25519",
      "kid": "jZ_n4xoC3-0EybHsYhwNoXmklqvBKiKg_PagUwwD95E",
      "kty": "OKP",
      "use": "sig",
      "x": "eU_f9AN7-aW9wZp-WOPskXApWHdH7OhwJwUG0_kT8C8"
    }
  ]
}
```

Referenced by the `ku=` parameter in the `_dnsid` TXT record. This is a **different Ed25519 key** from the agent key — it belongs to the `webmesh.ai` organization entity, not the `agent.webmesh.ai` agent instance.

| Property | Agent Key | Org Key |
|----------|-----------|---------|
| Domain | `agent.webmesh.ai` | `webmesh.ai` |
| kid | `ocmJWjyVDuMUiyZMa6pOGrgx_dZhSnSDsz35hmN-k9k` | `jZ_n4xoC3-0EybHsYhwNoXmklqvBKiKg_PagUwwD95E` |
| x (pubkey) | `ihnMrBbo3WFd7wCOKBkcOr73CrAgkGIEaAQryO0OtS8` | `eU_f9AN7-aW9wZp-WOPskXApWHdH7OhwJwUG0_kT8C8` |

---

### 2.14 `/mcp` — MCP Endpoint

Returns **406 Not Acceptable** on GET. This is a **JSON-RPC over streamable-HTTP** endpoint (POST only). Transport: `streamable-http`, MCP version `2025-03-26`.

**Tools exposed:**
1. `verify` — same as the A2A skill
2. `discover` — same as the A2A skill
3. `interact` — same as the A2A skill

To call: send JSON-RPC POST requests per the MCP streamable-HTTP transport spec.

---

### 2.15 `/llms.txt`

Plain-text summary of the agent's capabilities for LLM consumption. Contains the same info as the HTML page in text form: agent name, description, ANS name, access (no credential required), skills list, and interoperability mechanisms.

---

## 3. DNS Records (live verified via Google DoH)

All records below were fetched live from `dns.google/resolve` on 2026-09-19. DNSSEC validation status (`AD` flag) noted where applicable.

### 3.1 SVCB (DNS-AID)

```
agent.webmesh.ai  IN  SVCB  1 . alpn=a2a,h2
TTL: 3600  |  AD: true (DNSSEC validated)
```

The `alpn=a2a,h2` value advertises that this host speaks the A2A protocol (and HTTP/2). This is the DNS-AID discovery mechanism: any DNS client can discover the agent's protocol support without an HTTP round-trip.

### 3.2 TXT `_ans.agent.webmesh.ai`

```
v=ans1; version=v1.0.13; p=a2a; mode=direct; url=https://agent.webmesh.ai
TTL: 600
```

ANS discovery record. Fields: `v=ans1` (ANS version 1), `version` (agent version), `p=a2a` (protocol), `mode=direct` (direct connection, no proxy), `url` (agent endpoint).

### 3.3 TXT `_ans-badge.agent.webmesh.ai`

```
v=ans-badge1; version=v1.0.13; url=https://transparency.ans.godaddy.com/v1/agents/de02d013-138e-4e50-9cae-daa20bc3dc37
TTL: 600
```

Points to the agent's transparency log entry (GoDaddy ANS Transparency Log). The URL returns a full merkle proof and registration attestations.

### 3.4 TXT `_dnsid.agent.webmesh.ai` — DNSid Record

```
v=DNSid1;cu=https://agent.webmesh.ai/.well-known/agent-card.json;ku=https://webmesh.ai/.well-known/dnsid/entity-keys.json;lr=scitt:https://transparency.ans.godaddy.com/v1/agents/de02d013-138e-4e50-9cae-daa20bc3dc37/receipt;oi=webmesh.ai;su=https://agent.webmesh.ai/.well-known/dnsid/status.json;sg=7g18jFhuGHPYuaaQLKlktw9_PzY27Te05zLCdMht72PAW2nN2yxLty4XC1aT2hA6dnzM_ACI96OL_eEeZZTYBQ
TTL: 600
```

**DNSid field breakdown:**

| Parameter | Value | Meaning |
|-----------|-------|---------|
| `v` | `DNSid1` | DNSid protocol version 1 |
| `cu` | `https://agent.webmesh.ai/.well-known/agent-card.json` | Credential URL (the agent card) |
| `ku` | `https://webmesh.ai/.well-known/dnsid/entity-keys.json` | Key URL (org-level JWKS, note: `webmesh.ai` not `agent.webmesh.ai`) |
| `lr` | `scitt:https://transparency.ans.godaddy.com/...receipt` | Ledger reference (SCITT receipt URL) |
| `oi` | `webmesh.ai` | Organization identifier |
| `su` | `https://agent.webmesh.ai/.well-known/dnsid/status.json` | Status URL |
| `sg` | `7g18jFhuGHPYuaaQLKlktw9_...` | Ed25519 signature over the record |

### 3.5 TLSA `_443._tcp.agent.webmesh.ai` — DANE

```
3 0 1 6504d16a980d7da0162e5c3bad81f736e1e7db0a447180de6e5a944f597b7e80
```

> **Note:** This record did NOT resolve via Google DoH (returned SOA authority only). However, the hash `6504d16a...` is confirmed in the transparency log attestations (`dane_tlsa_provisioned`), proving it exists in the authoritative DNS. The `3 0 1` usage means: match the end-entity certificate (3), full certificate (0), SHA-256 hash (1).

### 3.6 DNSSEC

The zone is DNSSEC-signed. The `AD` (Authenticated Data) flag is `true` on SVCB lookups via Google DoH, confirming DNSSEC validation. Full chain viewable at `dnsviz.net/d/agent.webmesh.ai`.

### 3.7 Transparency Log Entry

The full transparency log at `https://transparency.ans.godaddy.com/v1/agents/de02d013-138e-4e50-9cae-daa20bc3dc37` contains:

```
merkleProof:
  leafHash: <base64>
  leafIndex: 209721
  path: [19-element array of tree nodes]
  rootHash: <base64>
  rootSignature: <ES256 JWS compact>
  treeSize: 348534

payload:
  event: AGENT_REGISTERED
  attestations:
    - dns_svcb_provisioned (alpn=a2a,h2)
    - dns_ans_txt_provisioned
    - dns_ans_badge_txt_provisioned
    - dns_dnsid_txt_provisioned
    - dane_tlsa_provisioned (hash: 6504d16a...)
    - domain_validation_method: ACME-DNS-01
    - identity_cert_fingerprint: <sha256>
    - server_cert_fingerprint: <sha256>
    - cert_not_before / cert_not_after dates

schema: V1
status: WARNING
```

---

## 4. Identity & Crypto Summary

### Agent Key (used across all agent-level identity layers)

| Property | Value |
|----------|-------|
| Key type | Ed25519 (OKP) |
| Public key (base64url) | `ihnMrBbo3WFd7wCOKBkcOr73CrAgkGIEaAQryO0OtS8` |
| Key ID (kid) | `ocmJWjyVDuMUiyZMa6pOGrgx_dZhSnSDsz35hmN-k9k` |
| DID | `did:web:agent.webmesh.ai` |
| SPIFFE ID | `spiffe://webmesh.ai/agents/agent` |
| ANS name | `ans://v1.0.13.agent.webmesh.ai` |
| ANS agent UUID | `de02d013-138e-4e50-9cae-daa20bc3dc37` |
| X.509 issuer | GoDaddy Private ANS Issuing CA - PR1v1 |
| Cert validity | 2026-09-12 to 2027-09-12 |
| Signing algorithm | EdDSA |
| Agent card JWS typ | `agent-card+jws` |

This same key appears in: agent-card.json `signatures[]`, trust-card.json `keys[]`, jwks.json, did.json `verificationMethod[]`, signature-agent-card, and http-message-signatures-directory.

### Organization Key (DNSid entity-level, different from agent key)

| Property | Value |
|----------|-------|
| Domain | `webmesh.ai` (not `agent.webmesh.ai`) |
| Key type | Ed25519 (OKP) |
| Public key (base64url) | `eU_f9AN7-aW9wZp-WOPskXApWHdH7OhwJwUG0_kT8C8` |
| Key ID (kid) | `jZ_n4xoC3-0EybHsYhwNoXmklqvBKiKg_PagUwwD95E` |
| Served at | `https://webmesh.ai/.well-known/dnsid/entity-keys.json` |
| Referenced by | `ku=` parameter in `_dnsid` TXT record |

This key provides organizational accountability — it proves that `webmesh.ai` (the org) vouches for `agent.webmesh.ai` (the agent).

---

## 5. Ecosystem Agents

### 5.1 `travel.agenthaven.dev` — Agent Bench Travel Merchant

| Field | Value |
|-------|-------|
| Name | Agent Bench Travel Merchant |
| URL | `https://travel.agenthaven.dev/a2a` |
| Version | 0.1.0 |
| Protocol | A2A 0.3 (JSON-RPC) |
| MCP | `https://travel.agenthaven.dev/mcp` (MCP 2025-06-18) |
| Provider | Agent Bench / agenthaven.dev |
| Skills | `flight-search` (open), `travel-checkout` (bearer JWT required) |
| Auth | Bearer JWT for checkout; search is open |

### 5.2 `supplier.webmesh.ai` — Travel Supplier

| Field | Value |
|-------|-------|
| Name | Travel Supplier |
| URL | `https://supplier.webmesh.ai` |
| Version | 1.0.3 |
| Protocol | A2A 1.0 (JSON-RPC) |
| MCP | `https://supplier.webmesh.ai/mcp` (streamable-http, 2025-03-26) |
| Provider | Webmesh |
| DID | `did:web:supplier.webmesh.ai` |
| SPIFFE | `spiffe://webmesh.ai/agents/supplier` |
| ANS | `ans://v1.0.3.supplier.webmesh.ai` |
| Skills | `get_quote` (x402-gated), `book_flight` (AP2 mandate + DPoP + EIP-3009) |
| Auth | noAuth declared; mandate-based for booking |

### 5.3 `fraud.webmesh.ai` — Fraud Test Agent

| Field | Value |
|-------|-------|
| Name | Fraud Test Agent |
| URL | `https://fraud.webmesh.ai` |
| Version | 1.0.2 |
| Protocol | A2A 1.0 (JSON-RPC) |
| MCP | `https://fraud.webmesh.ai/mcp` (streamable-http, 2025-03-26) |
| Provider | Webmesh |
| DID | `did:web:fraud.webmesh.ai` |
| SPIFFE | `spiffe://webmesh.ai/agents/fraud` |
| ANS | `ans://v1.0.2.fraud.webmesh.ai` |
| Skills (16 total) | `run_battery`, `replay_booking`, `underpay_booking`, `tamper_mandate`, `underpay_valid_sig`, `quote_swap_attack`, `wrong_audience_attack`, `wrong_scope_attack`, `wrong_dpop_key_attack`, `corrupt_jws_attack`, `superseded_format_attack`, `unknown_key_mandate`, `replay_settled`, `canonicalization_probe`, `payto_binding_check`, `card_drift_watch` |

---

## 6. Architecture Summary for Replication

To build an identical agent, you need to serve these endpoints:

### Required files (static JSON)

1. **`/.well-known/agent-card.json`** — A2A identity. Must include: `name`, `url`, `description`, `version`, `protocolVersion`, `provider` (with `did`), `supportedInterfaces`, `capabilities` (with `extensions` for MCP and trust stack), `securitySchemes`, `securityRequirements`, `defaultInputModes`, `defaultOutputModes`, `skills[]`, `x-identity` (ANS + WIMSE), `x-discovery`, `signatures[]`.

2. **`/.well-known/ans/trust-card.json`** — Trust identity. Must include: `ansName`, `agentDisplayName`, `version`, `agentHost`, `endpoints[]`, `keys[]` (with `x5c`), `agentId`, `transparencyReceipt`, `botProfile`.

3. **`/.well-known/mcp.json`** — MCP discovery. Fields: `mcpVersion`, `version`, `endpoint`, `transport`, `authentication`, `tools[]`, `agentCardUrl`, `trustCardUrl`.

4. **`/.well-known/did.json`** — DID document. Fields: `@context`, `id`, `verificationMethod[]`, `authentication`, `assertionMethod`, `service[]`.

5. **`/.well-known/ard.json`** and **`/.well-known/ai-catalog.json`** — ARD catalog. Fields: `specVersion`, `host` (with `trustManifest`), `entries[]` (one per interface: A2A, MCP, NANDA), `extensions`.

6. **`/.well-known/jwks.json`** — JWKS with your Ed25519 public key.

7. **`/.well-known/signature-agent-card`** — Serves the same JSON as the trust card. Used as the `jku` in the agent card's JWS `protected` header.

8. **`/.well-known/http-message-signatures-directory`** — Serves the same JSON as the trust card. Public key directory for RFC 9421 HTTP Message Signatures.

9. **`/.well-known/dnsid/status.json`** — Returns `{"status":"ACTIVE"}`. Referenced by `su=` in the DNSid TXT record.

10. **`/agentfacts.json`** — NANDA profile. Fields: `agent_name`, `label`, `description`, `version`, `documentationUrl`, `jurisdiction`, `provider`, `endpoints`, `capabilities`, `skills[]`, `telemetry`, `certification`.

11. **`/health`** — Health endpoint returning `{"status":"ok","a2aProtocolVersion":"1.0","mcpProtocolVersion":"2025-03-26"}`.

12. **`/robots.txt`** — With `Agentmap:` pointing to your ai-catalog.json.

13. **`/llms.txt`** — Plain text summary.

14. **`/`** — HTML info page.

### Required org-level endpoint (on your org domain, not the agent subdomain)

15. **`<org-domain>/.well-known/dnsid/entity-keys.json`** — Org-level JWKS with a **separate** Ed25519 key. Referenced by the `ku=` parameter in the DNSid TXT record. For `agent.webmesh.ai`, this is served from `webmesh.ai`.

### Required service endpoint

16. **`/mcp`** — JSON-RPC over streamable-HTTP (POST). Implements MCP protocol version `2025-03-26`. Exposes your tools via the standard MCP `tools/list` and `tools/call` methods.

### DNS records to provision

- SVCB: `<host> IN SVCB 1 . alpn=a2a,h2`
- TXT `_ans.<host>`: `v=ans1; version=<ver>; p=a2a; mode=direct; url=https://<host>`
- TXT `_ans-badge.<host>`: `v=ansbadge1; version=<ver>; url=<transparency log URL>`
- TXT `_dnsid.<host>`: `v=DNSid1;cu=<agent-card-url>;ku=<org-entity-keys-url>;lr=scitt:<receipt-url>;oi=<org-domain>;su=<status-url>;sg=<ed25519-signature>`
- TLSA `_443._tcp.<host>`: `3 0 1 <sha256 server cert fingerprint>`
- DNSSEC: sign the zone

### Crypto requirements

- Generate **two** Ed25519 keypairs: one agent-level, one org-level
- Use the agent key for: agent card JWS signatures, trust card `keys[]`, JWKS, DID document `verificationMethod[]`, HTTP Message Signatures (signature-agent-card + http-message-signatures-directory), WIMSE tokens
- Use the org key for: `entity-keys.json` on the org domain, DNSid `sg=` signature, and the `ku=` reference in the DNSid TXT record
- Obtain an X.509 identity cert from an ANS Registration Authority (or self-sign for dev)
- Sign the agent card as a detached JWS with `typ: "agent-card+jws"` and `alg: "EdDSA"`
- The trust card JSON is served at three paths: `/.well-known/ans/trust-card.json`, `/.well-known/signature-agent-card`, and `/.well-known/http-message-signatures-directory`
