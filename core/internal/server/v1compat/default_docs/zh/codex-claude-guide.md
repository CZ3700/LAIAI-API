# Codex与Claude Code接入指南

针对 AI Agent 客户端（如 Codex、Claude Code 等），本指南详细说明如何在客户端中配置 laiai 网关接口、设置模型路由代理以及配置账户额度与余额查询。

---

## 一、 Claude Code 接入

### 1. 接口与模型配置
* **Claude 系列模型**：支持使用标准 `/v1/messages` 接口。配置参考下图：

  ![Claude Code Messages 接口配置](/assets/docs/codex-claude/image1.png)

* **高级模型（如 GPT-5.6 / Grok-4.5）**：建议使用 `/v1/responses` 接口。配置参考下图：

  ![Claude Code Responses 接口配置](/assets/docs/codex-claude/image2.png)

  > [!TIP]
  > 在客户端设置中，点击“获取模型列表”后才会出现可调用的模型下拉菜单。

### 2. 代理与路由设置
* 添加模型通道后，请确保勾选并**开启代理**：

  ![开启代理设置](/assets/docs/codex-claude/image3.png)

* 另外请检查是否启用了**智能路由**：

  ![路由启用检查](/assets/docs/codex-claude/image4.png)

### 3. 额度查询配置
如需在客户端中实时显示 laiai 账户余额与额度消耗，可配置如下提取器函数：

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
        planName: "用户账户余额",
        remaining: response.remain_balance,
        used: response.used_balance,
        total: (response.remain_balance + response.used_balance),
        unit: "CNY"
      };
    }
    return {
      isValid: false,
      invalidMessage: response.message || "查询失败"
    };
  }
})
```

---

## 二、 Codex 接入

### 1. 接口与模型配置
* **GPT-5.6 / Grok-4.5 等新一代大模型**：推荐使用 `/v1/responses` 接口。配置参考下图（需点击“获取模型列表”展示下拉菜单）：

  ![Codex Responses 接口配置](/assets/docs/codex-claude/image5.png)

* **通用模型（如 DeepSeek、GLM 等）**：可以使用标准 `/v1/chat/completions` (Chat) 接口。配置参考以下截图：

  ![Codex Chat 接口配置1](/assets/docs/codex-claude/image6.png)

  ![Codex Chat 接口配置2](/assets/docs/codex-claude/image7.png)

> [!WARNING]
> **关于 Claude 系列模型的接入注意事项**：
> 平台上的 Claude 模型也支持 Chat 接口；但 Codex 客户端可能带入 Claude 不兼容的参数，因此 **Codex 请勿用 Chat 接口调 Claude**，请改用 Claude 专用接口。

### 2. 余额查询配置
Codex 客户端查询账户余额的提取器脚本如下：

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
        planName: "用户账户余额",
        remaining: response.remain_balance,
        used: response.used_balance,
        total: (response.remain_balance + response.used_balance),
        unit: "USD"
      };
    }
    return {
      isValid: false,
      invalidMessage: response.message || "查询失败"
    };
  }
})
```

---

## 三、 常见问题与提示 (QA)

### 1. 为什么 Agent 客户端提示词缓存消耗较高？
Codex 和 Claude Code 等 Agent 客户端在发起请求时，会默认附带较长的 System Prompt（系统提示词）与工具定义。因此即使单次会话输入/输出字符较少，也可能由于前置 Prompt 较大产生一定的 Prompt Token 消耗。

![Agent 系统提示词与缓存机制](/assets/docs/codex-claude/image8.png)

### 2. 缓存命中条件
Context Caching（上下文缓存）通常需在**多轮连续对话**后才会命中并享受缓存折扣；单轮独立请求一般不会命中。
