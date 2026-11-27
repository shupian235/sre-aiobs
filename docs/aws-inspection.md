# AWS 云端巡检

AIOps 通过 AWS SDK 读取指定区域的 CloudWatch 指标告警。适用于已创建 CloudWatch 告警的 EC2、RDS、ALB、EKS 等资源。没有配置 CloudWatch 告警的资源不会出现在该巡检结果中。

## 凭据与权限

优先使用实例、容器或工作负载的 IAM Role。使用 Access Key 时，在部署环境、受保护的 `.env` 或 Kubernetes Secret 注入：

```dotenv
AWS_ACCESS_KEY_ID=替换为你的AccessKeyID
AWS_SECRET_ACCESS_KEY=替换为你的SecretAccessKey
# 使用 STS 临时凭据时必须同时注入
# AWS_SESSION_TOKEN=替换为你的临时SessionToken
```

AWS 凭据不得提交到 Git。AWS 巡检和 SES 都通过现有 AWS SDK 凭据链；需要两者时，部署身份应同时具备各自所需的权限。巡检读取 CloudWatch 所需的最小策略示例：

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "cloudwatch:DescribeAlarms",
      "Resource": "*"
    }
  ]
}
```

可选的账号匹配检查使用 STS `GetCallerIdentity`。应用不重新生成 Access Key，也不修改或删除云资源。

## 配置来源

在部署机 `config.yaml` 的现有 `sources` 列表中添加：

```yaml
- name: aws-prod-singapore
  kind: aws
  environment: production
  region: ap-southeast-1
  aws_account_id: "123456789012" # 可选，填写真实账号 ID
  alarm_prefix: "prod-"          # 可选，只检查名称以前缀开头的告警
  check_timeout: 4s
```

需要多个区域时，每个区域使用一个独立的来源名称。同一应用实例的各区域使用同一套 AWS 凭据。更新部署环境后，按原选项重启应用，在 **日志数据源 → 检查** 验证 CloudWatch 读取权限。设置账号 ID 后，账号不符时不会查询告警。

## 查看巡检

AWS 来源按 5 分钟、小时、日任务查看当次读取到的 CloudWatch 状态。结果为采集时快照。每次查询按页读取，遇到权限错误或超出页数上限时标记任务失败。

| CloudWatch 状态 | 运维看板 |
| --- | --- |
| `ALARM` | 生成风险评分 90 的事件 |
| `INSUFFICIENT_DATA` | 生成风险评分 65 的事件 |
| `OK` | 计入已检查数量 |

在安全概览按 AWS 来源筛选风险事件，点击详情查看告警名，在 **任务记录 → Run ID** 查看检查数量并导出报告。启用 Web「邮件小时报告与日报」后，AWS 巡检结果随现有 SES HTML 报告发送。AWS 巡检未使用分钟任务，因此不会走 Slack 或 Telegram 的分钟告警链路。

巡检统计复用 PostgreSQL 快照和风险表。升级时应用 `004_inspection_report_indexes.sql`，为任务时间窗口及风险事件查询建立索引。已有大表应安排维护窗口，索引创建期间会锁定相关表的写入。报告与 HTML 邮件显示检查数、正常、告警及采样不足的数量。
