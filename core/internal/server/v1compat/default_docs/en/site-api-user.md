# Account & Wallet APIs

This section includes **direct balance query endpoints using an API Key (`sk-xxx`)** (no login required, ideal for runtime clients) and **console account/wallet management endpoints using a JWT token**.

---

## Part 1: Direct Balance Query via API Key (`sk-xxx`)

Clients can query token quota and user account balance directly at runtime by passing `Authorization: Bearer sk-your_token`.

### 1. Query Current Token Quota (Token Balance)
Query the remaining sub-quota and used amount of the current API key.
* **Endpoint**: `/v1/balance`
* **Method**: `GET`
* **Headers**: `Authorization: Bearer sk-your_token`
* **Response Example**:
```json
{
  "success": true,
  "remain_balance": 985.42,
  "used_balance": 14.58,
  "unlimited_quota": false
}
```
* *Note: When the token is configured with unlimited quota, `remain_balance` returns `-1` and `unlimited_quota` is `true`.*

### 2. Query User Account Balance (User Balance)
Query the overall available account balance of the user who owns this API key (rather than a single token's sub-quota limit).
* **Endpoint**: `/v1/user/balance`
* **Method**: `GET`
* **Headers**: `Authorization: Bearer sk-your_token`
* **Response Example**:
```json
{
  "success": true,
  "remain_balance": 500.00,
  "used_balance": 24.50,
  "unlimited_quota": false
}
```

---

## Part 2: Console Account & Wallet Management (JWT Auth)

These endpoints require a login JWT token (`Authorization: Bearer <JWT>`) to retrieve profile details, wallet statistics, recharge records, and transfer affiliate commissions.

### 1. Get User Profile
* **Endpoint**: `/api/v1/user/profile`
* **Method**: `GET`
* **Response Example**:
```json
{
  "id": "1",
  "username": "user1",
  "email": "user@example.com",
  "mobile": "13800000000",
  "role": "user",
  "user_group": "default",
  "is_active": 1,
  "created_at": "2026-06-01T12:00:00Z"
}
```

### 2. Get Wallet Balance and Statistics
* **Endpoint**: `/api/v1/user/wallet`
* **Method**: `GET`
* **Response Example**:
```json
{
  "balance": 500.00,
  "spent": 24.50,
  "affiliate_commission": 15.00,
  "user_level": "VIP 1",
  "discount_rate": 0.95
}
```

### 3. Get Recharge Records
* **Endpoint**: `/api/v1/user/recharge_records`
* **Method**: `GET`
* **Response Example**:
```json
{
  "data": [
    {
      "id": "1001",
      "amount": 100.00,
      "method": "wechat",
      "status": "success",
      "created_at": "2026-06-15T09:30:00Z"
    }
  ],
  "total": 1
}
```

### 4. Transfer Affiliate Commission
* **Endpoint**: `/api/v1/user/affiliate/transfer`
* **Method**: `POST`
* **Request Payload**:
```json
{
  "amount": 10.00
}
```
* **Response Example**:
```json
{
  "message": "Commission transferred successfully",
  "new_balance": 510.00,
  "remaining_commission": 5.00
}
```
