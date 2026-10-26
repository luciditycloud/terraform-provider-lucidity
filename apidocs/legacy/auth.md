---
title: "Getting Started"
slug: "lucidity-api-getting-started"
updated: 2026-04-27T06:51:42Z
published: 2026-04-27T06:51:42Z
canonical: "docs.lucidity.cloud/lucidity-api-getting-started"
stale: true
---

> ## Documentation Index
> Fetch the complete documentation index at: https://docs.lucidity.cloud/llms.txt
> Use this file to discover all available pages before exploring further.

# Getting Started

## **Prerequisites Checklist**

Before you begin, ensure you have:

- Cloud instances already managed by Lucidity
- Basic understanding of REST APIs and JSON
- Secure storage solution for API credentials (recommended)
- Development environment for testing API calls

### **Quick Start**

Follow these steps to make your first API call:

Each step serves a specific purpose:

- **Authentication:** Secure access using two-token system
- **Install:** Trigger the agent installation on your target cloud instance(s)
- **Track:** Monitor the installation job using the returned job ID

### **Authentication**

**Note :** Authentication is a required first step for all API calls.

### **Security Model Overview**

**Step 1: Getting Your Refresh Token**

1) Log in to the Lucidity dashboard with an account that has the Admin role and navigate to the Users section.
Note: This feature is available only to users with the Admin role. Admins can generate API keys only for their own account. Generating API keys for other users is not supported.

2) Navigate to your user (admin role) entry. Click the three-dot action menu.

3) Locate the Generate New Key button and click on it. Note : The option to generate an API key is visible only within your own account view and does not appear for other users.

4) Fill in the required details and click Generate.

5) Copy the key immediately and store it securely — it will not be shown again.

**Security Best Practices:**

- Use a credential manager (HashiCorp Vault, AWS Secrets Manager, etc.)
- Monitor token expiry (30-day default)

**Step 2: Getting Access Tokens**

Access tokens are required for every API call and expire after 15 minutes.

**ENDPOINT**

POST /external/api/v1/auth/user-token/refresh

**Request Example:**

```bash
curl -X POST https://dash-back.lucidity.dev/external/api/v1/auth/user-token/refresh \
  -H "Content-Type: application/json" \
  -d '{
    "refreshToken": "your-refresh-token-here"
  }'
```

**Response:**

```json
{
  "accessToken": "eyJhbGciOiJIUzI1NiIsInR5I6IkpXVCJ9..."
}
```

**Step 3: Using Access Tokens**

Include these required headers in all API requests:

| **HEADER** | **VALUE** | **DESCRIPTION** |
| --- | --- | --- |
| X-Authtype | lucidity_access_token | Specifies the type of authentication being used. |
| Content-Type | application/json | Indicates that the request body is in JSON format. |
| Authorization | {{access-token}} | Replace {{access-token}} with your current short-lived access token. |

## **API Reference**

Your API base URL depends on where you access the Lucidity dashboard:

| **Dashboard Login URL** | **API Base URL** |
| --- | --- |
| https://www.web.lucidity.dev/dashboard | https://dash-back.lucidity.dev |
| https://web-azurepls.lucidity.cloud/dashboard | https://dashboard-azurepls.lucidity.cloud |
| https://app.lucidity.cloud | https://app.lucidity.cloud |
| https://in.app.lucidity.cloud | https://in.app.lucidity.cloud |
| https://eu.app.lucidity.cloud | https://eu.app.lucidity.cloud |


--

---
title: "Refresh Access Token API -  Reference"
slug: "get-a-new-access-token-using-a-refresh-token-1"
updated: 2026-04-22T07:11:17Z
published: 2026-04-22T07:11:17Z
canonical: "docs.lucidity.cloud/get-a-new-access-token-using-a-refresh-token-1"
stale: true
---

> ## Documentation Index
> Fetch the complete documentation index at: https://docs.lucidity.cloud/llms.txt
> Use this file to discover all available pages before exploring further.

# Refresh Access Token API -  Reference

Post/external/api/v1/auth/user-token/refresh

Exchanges a long-lived refresh token for a short-lived access token. Access tokens expire after 15 minutes and are required for all secured API calls.

SecurityAPI Key: LucidityAccessTokenHeader parameter nameAuthorization

Short-lived Lucidity access token obtained via the refresh endpoint. Must be used together with X-Authtype: lucidity_access_token.

Body parameters<select class='api-response-data' aria-label='Media type'><option value='b6b668a3-48e0-49ea-8a0c-3b5a99d1c223'>application/json</option>
</select>object  refreshTokenstring    Required

Long-lived refresh token issued by Lucidity CS.

Responses200

Access token successfully issued.

<select class='api-response-data' aria-label='Media type'><option value='15eed26c-6628-4558-a245-9bfeee9de4dc'>application/json</option>
</select>object  accessTokenstring    

Short-lived access token (expires after 15 minutes).

400

Invalid refresh token request.

<select class='api-response-data' aria-label='Media type'><option value='8eeae975-d83d-46ee-8859-1c2beaa35c9a'>application/json</option>
</select>Expand Allobject  codestring    

Application-specific error code.

messagestring    

Human-readable description of the error.

detailsobject  

Optional structured error details.

401

Invalid or expired refresh token.

<select class='api-response-data' aria-label='Media type'><option value='7f9a92f8-9570-472e-be75-fbe769111d0f'>application/json</option>
</select>Expand Allobject  codestring    

Application-specific error code.

messagestring    

Human-readable description of the error.

detailsobject  

Optional structured error details.
