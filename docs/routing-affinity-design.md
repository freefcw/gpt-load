# 权重调度与会话亲和设计

## 1. 背景

GPT-Load 的账号选择同时受到权重调度、凭据健康状态、Responses 续接和 WebSocket 连接绑定影响。过去的实现把提示词前缀也作为软账号亲和信号，并在调度器中用 PreferredCredentialID 无条件覆盖加权选择，导致以下现象：

- 某个 prompt/cache key 一旦绑定到账号，后续请求长期优先进入该账号；
- 提高另一个账号的权重不能立即改变已有软亲和映射；
- Codex 的 previous_response_id 和 WebSocket turn 本身是硬绑定，不能按普通请求比例统计；
- 同一个分组内账号的权重看起来不生效，实际是请求没有进入普通加权调度路径。

本设计将“会话连续性”和“流量分配”分层，保留必须的状态绑定，同时让没有硬状态约束的请求真正受到权重控制。

## 2. 设计目标与不变量

### 2.1 硬绑定必须优先

以下场景不能静默切换凭据：

1. previous_response_id 已经绑定到某个凭据；
2. Responses WebSocket 已经建立上游连接；
3. 其他明确要求上游状态连续的绑定。

硬绑定失败时应返回明确的不可用/限额结果，而不是把请求发送到另一个账号并破坏会话语义。

### 2.2 会话入口使用权重，turn 内不重新均衡

同一个有状态会话应该在首次进入时按照权重选择账号，后续 turn 固定到该账号。权重的作用对象是“会话入口”而不是“同一会话的每一个 turn”。

因此：

- 100 个独立会话可以按 100:1:1 分配；
- 一个会话包含 100 个 turn 时，100 个 turn 固定到一个账号是预期行为；
- 一个长连接的 turn 数不能直接当作账号负载均衡请求数。

### 2.3 提示词前缀不是 Codex 会话身份

提示词前缀可以继续作为 provider-private continuity/cache 信号，但不能默认作为 Codex 账号路由 owner。Codex 的账号路由亲和只接受显式的 prompt_cache_key 或 Session ID 等明确会话标识。

## 3. 权重算法

有效权重为：

~~~text
effective_weight = group_weight * credential_weight
~~~

未配置权重的默认值是 50。同一分组内，分组权重是公共乘数，账号之间的比例由账号权重决定。

调度器使用共享的加权公平队列：

~~~text
选择虚拟进度最小的凭据
选择后 progress += 1 / effective_weight
~~~

该算法是平滑的、并发安全的，并且按“调度尝试”计账，而不是按成功响应或 token 数计账。失败重试也会消耗一次候选选择名额。

## 4. 三层路由模型

### 4.1 硬状态绑定

| 信号 | 行为 |
| --- | --- |
| previous_response_id | 只允许原绑定凭据 |
| WebSocket connection | 连接生命周期内固定原凭据 |
| 硬续接失败 | 不静默切换到其他账号 |

### 4.2 显式会话亲和

真正的会话标识（例如显式 Session ID）在第一次成功请求时记录 owner：

~~~text
新会话 -> 加权调度 -> 成功后记录 key -> credential owner
~~~

相同会话标识的后续请求继续使用 owner。账号权重变化、账号配置变化或亲和策略 revision 变化后，软映射失效；硬绑定不受影响。

prompt_cache_key 默认只作为上游缓存提示，不等同于会话身份。对于包含 Codex 凭据的候选池，它不会创建账号 owner，避免多个独立会话共享一个缓存键时把流量锁死在一个账号上。完全不包含 Codex 的候选池仍保留原有缓存亲和行为。

### 4.3 普通请求

没有硬绑定、没有显式会话 key 的请求直接进入共享加权调度，不允许由提示词前缀生成的软映射覆盖权重。

## 5. 亲和信号分类

请求日志和内部路由决定应区分以下类型：

~~~text
none
prompt_prefix
prompt_cache_key
session_id
response_continuity
websocket_binding
~~~

其中：

- prompt_prefix：Codex 默认只作为 provider continuity，不作为账号路由 owner；
- prompt_cache_key / session_id：软会话 owner；
- response_continuity / websocket_binding：硬绑定；
- 日志中的软亲和命中不能与硬绑定命中混为一谈。

## 6. 缓存生命周期与配置变更

亲和缓存采用以下失效条件：

1. 当前运行配置 revision 变化；
2. 凭据配置 revision 变化，包括账号权重、启停、身份和分组变化；
3. TTL 到期；
4. 达到最大生命周期。

现有滑动 TTL 会在每次成功请求后持续刷新，可能导致热门 key 永久占用一个账号。后续应支持 idle TTL 与 max lifetime 两个边界；max lifetime 只作用于软亲和，不强制迁移硬绑定会话。

## 7. 推荐配置

第一阶段对 Codex 使用：

~~~text
硬续接：保持绑定
WebSocket：保持连接级绑定
显式 Session ID：会话级 owner
prompt_cache_key：只作为上游缓存提示，不作为 Codex 账号 owner
只有提示词前缀：不做账号路由粘性
无 key：普通加权调度
~~~

如果未来需要兼容旧的“软亲和无条件优先”行为，可以增加：

~~~text
affinity_mode = weighted | sticky
~~~

推荐默认 weighted；sticky 只作为显式兼容模式。

## 8. 观测与验证

需要在请求日志中观察：

- 最终凭据和每次 attempt 的凭据；
- affinity source；
- selection reason：weighted、soft_affinity、response_continuity、websocket_binding；
- 当前候选账号及有效权重；
- 是否因为 cooldown、auth、并发或本地 RPM 被排除。

验收场景：

1. 100:1:1 的独立无状态请求按权重分布；
2. 同一 prompt_cache_key 的请求保持会话 owner；
3. 多个不同 key 的会话按权重分布；
4. 相同提示词但没有显式 key 的 Codex 请求不再全部固定到一个账号；
5. previous_response_id 始终固定原账号；
6. 同一个 WebSocket 的所有 turn 始终固定原账号；
7. 修改账号权重后，新的软会话使用新权重，旧硬绑定不受影响。

## 9. 实施顺序

1. 增加 affinity source 分类，并对 Codex 禁用 prompt-prefix 账号亲和；
2. 增加凭据配置 revision，使账号权重变化可以失效软亲和；
3. 区分 WebSocket binding 与 Responses continuation 的日志类型；
4. 增加 scheduler、gateway、Codex 和 cache 回归测试；
5. 在生产观察独立会话分布后，再决定是否将 weighted affinity 暴露为全局/分组配置。

## 10. 当前实现状态

本仓库当前已落地第一阶段：

- OpenAI/Responses 请求支持受限的显式 Session-Id / Session_id 亲和 key；
- Codex 的提示词前缀仍可作为 provider continuity，但不会创建或命中账号路由 owner；
- 显式 Session ID 可建立软账号亲和；包含 Codex 的候选池中 prompt_cache_key 不再建立账号亲和；
- previous_response_id 和 WebSocket 连接绑定保持硬约束；
- 凭据配置 revision 会在账号权重、状态或集合变化时失效软亲和缓存；
- 请求日志新增 session_id 与 websocket_binding 亲和类型；
- 增加了对应的 affinity、dialect、registry、gateway 回归测试。

全局 affinity_mode=weighted|sticky 和 max lifetime 尚未打开为公共配置，避免一次性改变非 Codex 协议的历史行为；后续可以在观察第一阶段数据后单独灰度。
