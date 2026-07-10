-- 0043_apikey_route_policy.sql
-- api_keys 加 route_policy：key 级默认路由策略（设计 §4.4 策略化选路；指令 §5 provider preference）。
-- 枚举 ''|order|latency|throughput|price（空=平台默认 order）；每请求 X-LAIAI-Route 头优先于本列。
-- latency/throughput 按近 1h (model×channel) 统计在 priority 层内确定性取最优；
-- price 当前等价 order（单一平台定价无渠道差价，枚举保留待 BYOK/渠道差价）。
ALTER TABLE api_keys
    ADD COLUMN route_policy VARCHAR(16) NOT NULL DEFAULT '' COMMENT 'key 级路由策略，空=order 默认（0043）';
