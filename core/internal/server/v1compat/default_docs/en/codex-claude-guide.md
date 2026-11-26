# Codex & Claude Code Integration Guide

This guide details how to configure the laiai gateway endpoints, proxy routing, and account balance query extractors for AI Agent clients (such as Codex and Claude Code).

---

## 1. Claude Code Integration

### 1. Interface & Model Configuration
* **Claude Series Models**: Supports standard `/v1/messages` endpoint. Refer to the screenshot below:

  ![Claude Code Messages Interface Configuration](/assets/docs/codex-claude/image1.png)

* **Advanced Models (e.g., GPT-5.6 / Grok-4.5)**: Recommended to use `/v1/responses` endpoint. Refer to the screenshot below:

  ![Claude Code Responses Interface Configuration](/assets/docs/codex-claude/image2.png)

  > [!TIP]
  > Click "Fetch Model List" in the client settings to populate the selectable model dropdown menu.

### 2. Proxy & Routing Settings
* After adding model channels, make sure to check and **enable proxy**:

  ![Enable Proxy Settings](/assets/docs/codex-claude/image3.png)

* Please also check whether **smart routing** is enabled:

  ![Check Smart Routing](/assets/docs/codex-claude/image4.png)

### 3. Balance Query Configuration
To display real-time laiai balance and usage in the client, configure the following extractor function:

```javascript
({
  request: {
    url: "{{baseUrl}}/v1/user/balance",
    method: "GET",
    headers: {
      "Authorization": "Bearer {{apiKey}}"
    }
  },
  extractor: function (response) {
    if (response.success) {
      return {
        planName: "User Account Balance",
        remaining: response.remain_balance,
        used: response.used_balance,
        total: (response.remain_balance + response.used_balance),
        unit: "CNY"
      };
    }
    return {
      isValid: false,
      invalidMessage: response.message || "Query failed"
    };
  }
})
```

---

## 2. Codex Integration

### 1. Interface & Model Configuration
* **Next-Gen Models (GPT-5.6 / Grok-4.5)**: Recommended to use `/v1/responses` endpoint. Refer to the screenshot below (click "Fetch Model List" to show dropdown):

  ![Codex Responses Configuration](/assets/docs/codex-claude/image5.png)

* **General Models (DeepSeek, GLM, etc.)**: Can use standard `/v1/chat/completions` (Chat) endpoint. Refer to the screenshots below:

  ![Codex Chat Configuration 1](/assets/docs/codex-claude/image6.png)

  ![Codex Chat Configuration 2](/assets/docs/codex-claude/image7.png)

> [!WARNING]
> **Important Note regarding Claude Series Models**:
> Although Claude models on the platform support Chat endpoint compatibility at the gateway level, Codex client passes parameters incompatible with Claude when invoking. Therefore, **Codex client cannot connect to Claude models using the Chat endpoint**. Please use the dedicated Claude endpoint or gateway translation.

### 2. Balance Query Configuration
Extractor script for querying account balance in Codex client:

```javascript
({
  request: {
    url: "{{baseUrl}}/user/balance",
    method: "GET",
    headers: {
      "Authorization": "Bearer {{apiKey}}"
    }
  },
  extractor: function (response) {
    if (response.success) {
      return {
        planName: "User Account Balance",
        remaining: response.remain_balance,
        used: response.used_balance,
        total: (response.remain_balance + response.used_balance),
        unit: "USD"
      };
    }
    return {
      isValid: false,
      invalidMessage: response.message || "Query failed"
    };
  }
})
```

---

## 3. Frequently Asked Questions (QA)

### 1. Why is prompt cache usage relatively high on Agent clients?
Agent clients such as Codex and Claude Code include long System Prompts and tool definitions by default when sending requests. Even with minimal input/output per turn, the initial prompt overhead can result in noticeable prompt token usage.

![Agent System Prompt & Cache Mechanism](/assets/docs/codex-claude/image8.png)

### 2. Context Caching Trigger Conditions
Upstream Context Caching is typically triggered after **multiple consecutive turns of conversation**, allowing you to enjoy cache discounts. A single independent request will not trigger cache hits.
