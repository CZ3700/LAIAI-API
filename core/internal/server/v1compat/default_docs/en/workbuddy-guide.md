# WorkBuddy Integration Guide

This guide details how to integrate laiai with the **WorkBuddy** AI client, configuring OpenAI-compatible custom model channels, model capability flags, and using them efficiently in conversations.

---

## 1. Prerequisites

Before configuring WorkBuddy, log in to the laiai console to retrieve your credentials:

1. **API Base URL**: Found at the top of **Token Management** (e.g., `https://api.yourdomain.com`).
2. **API Key**: Click **Create API Token** under **Token Management** to generate and copy a key starting with `sk-`.
3. **Model Name (Model ID)**: Find the target model in **Model Plaza** and copy the full model ID (e.g., `doubao-seed-2-1-lite-260915`).

---

## 2. Step-by-Step Configuration

### Step 1: Open Settings and Navigate to Models

1. Open the WorkBuddy client and click **Models** under the **Features** section in the left sidebar.
2. In the **Custom Models** section on the right, click the **Add Model** button in the upper-right corner.

![Open Model Settings and Add Model](/assets/docs/workbuddy/image1.png)

> [!NOTE]
> Added model configurations are automatically saved to your local configuration file:  
> `%USERPROFILE%\.workbuddy\models.json`

---

### Step 2: Select Provider as "Custom"

1. In the **Add Model** modal, click the **Provider (OpenAI-compatible API only)** dropdown.
2. Scroll to the bottom under **Other**, and select **Custom**.

![Select Provider as Custom](/assets/docs/workbuddy/image2.png)

---

### Step 3: Configure Endpoint, API Key, and Model Parameters

Refer to the screenshot below and enter the parameters retrieved from your laiai console:

![Configure Endpoint and Parameters](/assets/docs/workbuddy/image3.png)

1. **Endpoint URL**: Enter the complete chat completions endpoint:
   ```text
   https://<your-domain>/v1/chat/completions
   ```
   > [!IMPORTANT]
   > The Base URL in the console only includes the domain (e.g. `https://api.example.com`). **You must append `/v1/chat/completions`**; otherwise, requests cannot be routed to the chat service.

2. **API Key**: Paste the `sk-` token key copied from Token Management.
3. **Connection Test**: Click **Test Connection** on the right. Proceed after seeing the green **Connected successfully** prompt.
4. **Model Name**: Paste the exact model name copied from Model Plaza (e.g. `doubao-seed-2-1-lite-260915`).
5. **Advanced Options** (select based on model capabilities):
   - **Tool Calling**: Check if the model supports Function / Tool Calling.
   - **Image Input**: Check if this is a multimodal Vision model.
   - **Thinking Mode / Allow Disabling Thinking**: Check for reasoning models (e.g. DeepSeek-R1, Thinking architecture) to render and control thinking chains.
6. Click **Save** in the bottom-right corner.

---

### Step 4: Select Model in Chat and Verify

1. Switch to the WorkBuddy chat conversation interface.
2. Click the model dropdown selector in the bottom-right corner of the input area.
3. Select the newly added model (e.g., `doubao-seed-2-1-lite-260915`).
4. Send any greeting message (e.g., "Hello"), verify the response, and confirm the model badge under the message shows your configured model.

![Select Model in Chat and Verify](/assets/docs/workbuddy/image4.png)

---

## 3. Frequently Asked Questions (FAQ)

### 1. "Test Connection" fails or times out?
* Verify that the endpoint ends with `/v1/chat/completions` and uses `https://`.
* Check if your network connection can access the laiai domain.

### 2. Getting 401 Unauthorized?
* Ensure your API token in Token Management is active, has sufficient balance, and has not expired.

### 3. Getting 404 Not Found or Model Not Found?
* Ensure the model name in WorkBuddy matches the Model ID in Model Plaza character-for-character.
