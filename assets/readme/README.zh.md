<h1 align="center"><a href="https://releem.com/"><img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem-icon-top.png" width="24" height="24" alt="Releem 标志" /></a> Releem</h1>

**结合 AI 与受控自动化的 MySQL 性能调优工具**

Releem 是一款结合 AI 与受控自动化的 MySQL 性能调优工具，也是从首次调优决策起持续协助您的数据库顾问。

适用于负责生产环境 MySQL 性能的工程师、DBA 和基础设施团队。通过基于工作负载的建议改善查询性能和资源使用效率，并随着查询、流量和数据的变化，重新评估需要关注的问题。

<p align="center">
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/MySQL-4479A1?logo=mysql&amp;logoColor=white" alt="MySQL" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/MariaDB-003545?logo=mariadb&amp;logoColor=white" alt="MariaDB" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/PostgreSQL-336791?logo=postgresql&amp;logoColor=white" alt="PostgreSQL" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/Percona_Server_for_MySQL-orange" alt="Percona Server for MySQL" /></a>
  <br />
  <a href="https://hub.docker.com/r/releem/releem-agent"><img src="https://img.shields.io/docker/pulls/releem/releem-agent?logo=docker&amp;logoColor=white" alt="Docker 拉取次数" /></a>
    <a href="https://docs.releem.com/installation"><img src="https://img.shields.io/badge/platforms-Linux%20%7C%20Windows%20%7C%20Docker%20%7C%20K8s%20%7C%20AWS%20%7C%20GCP%20%7C%20Azure%20%7C%20WHM%2FcPanel%20%7C%20Clusters-555555" alt="平台：Linux、Windows、Docker、Kubernetes、Amazon RDS、Amazon Aurora、Google Cloud SQL、Azure Database for MySQL、WHM/cPanel 和集群" /></a>
  <a href="https://github.com/Releem/releem-agent/stargazers"><img src="https://img.shields.io/github/stars/Releem/releem-agent?style=flat&amp;logo=github" alt="GitHub 星标数" /></a>
  <a href="https://github.com/Releem/releem-agent"><img src="https://img.shields.io/badge/Agent-open_source-2ea44f?logo=github" alt="开源 Agent" /></a>
  <a href="https://join.slack.com/t/releem-community/shared_invite/zt-1j3d0vosh-AJHbDiQrzVDvLat5eqQorQ"><img src="https://img.shields.io/badge/Slack-Join_community-4A154B?logo=slack&amp;logoColor=white" alt="Slack 社区" /></a>
</p>

<p align="center">
  <a href="../../README.md">English</a> •
  <a href="README.ru.md">Русский</a> •
  <a href="README.es.md">Español</a> •
  <a href="README.pt.md">Português</a> •
  <b>简体中文</b> •
  <a href="README.fr.md">Français</a> •
  <a href="README.id.md">Bahasa Indonesia</a>
</p>

<p align="center">
  <a href="#getting-started">开始使用</a> |
  <a href="https://docs.releem.com/">文档</a> |
  <a href="https://releem.com/mysql-configuration-tuning">MySQL 配置调优</a> |
  <a href="https://releem.com/sql-query-optimization">SQL 查询优化</a> |
  <a href="https://releem.com/compare/mysqltuner">与 MySQLTuner 比较</a> |
  <a href="#security">安全</a>
</p>

此仓库包含开源 **Releem Agent**，用于将数据库连接到 Releem 托管订阅服务。这里也收集整个 Releem 产品的反馈、Issues 和路线图讨论。

<p align="center">
  <img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem_dashboard.png" width="90%" alt="Releem 控制台" />
</p>

## ⚡ 为什么选择 Releem？

- **[查询与索引优化](https://releem.com/sql-query-optimization):** 查看基于工作负载的查询建议与[索引建议](https://docs.releem.com/recommendations)，以改善 MySQL 查询性能。
- **[MySQL 配置调优](https://releem.com/mysql-configuration-tuning):** 查看基于工作负载的设置，并通过机器学习增强调优，以改善资源使用效率。
- **受控自动化:** 通过文档说明的工作流程，应用受支持的配置变更以及已获批准且符合条件的数据库结构变更，让团队掌控变更内容和时机。
- **[监控与健康检查](https://releem.com/health-checks):** 使用数据库指标和健康检查，识别需要关注的问题，并比较变更后实际观测到的性能和资源使用情况。

有关受支持的变更及应用步骤，请参阅[配置调优过程](https://docs.releem.com/recommendations/configuration-tuning/mysql-tuning-process)和[数据库结构变更执行流程](https://docs.releem.com/recommendations/query-optimization/automatic-schema-changes)。

<p align="center">
  <strong>使用 Releem 查找并创建缺失的 MySQL 索引</strong><br /><br />
  <a href="https://www.youtube.com/watch?v=Pd-DUK0so9Y">
    <img src="https://img.youtube.com/vi/Pd-DUK0so9Y/hqdefault.jpg" width="480" alt="观看如何使用 Releem 查找并创建缺失的 MySQL 索引" />
  </a>
  <br />
  <a href="https://www.youtube.com/watch?v=Pd-DUK0so9Y">▶ 观看：使用 Releem 查找并创建缺失的 MySQL 索引</a>
</p>

<p align="center">
  <strong>应用建议的 MySQL 配置</strong><br /><br />
  <a href="https://youtu.be/XqBZPGRlrGs">
    <img src="https://raw.githubusercontent.com/Releem/docs/main/assets/images/applying-conf-dark.gif"
         width="80%"
         alt="Releem 控制台展示如何查看建议依据、检查是否需要重启，以及应用建议的 MySQL 配置" />
  </a>
  <br />
  <a href="https://youtu.be/XqBZPGRlrGs">▶ 在 YouTube 上观看 15 秒演示</a>
</p>

<p align="center">
  <strong>通过 Releem Agent CLI 应用配置</strong><br /><br />
  <img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem-applying.gif" width="80%" alt="使用 Releem Agent 应用配置" />
</p>

## 🧭 工作原理

1. **分析 MySQL 工作负载。** 收集查询活动、数据库设置和资源指标，以了解数据库的运行情况。
2. **识别性能调优机会。** 找出慢查询、频繁执行的查询，以及可能影响性能的配置问题。
3. **帮助确定处理优先级。** 利用工作负载观测结果和发现的问题，帮助选择优先处理的事项。
4. **建议查询、索引和配置变更。** 提供附带说明的改进建议，供团队审核。
5. **帮助应用受支持的变更。** 通过受控自动化，应用受支持的配置变更和已获批准的数据库结构变更。
6. **帮助衡量影响。** 比较变更后的查询性能、资源使用情况和实际生效的设置。

<a name="getting-started"></a>

## 🚀 开始使用 Releem

1. **创建账户。** [注册 Releem](https://releem.com/?utm_source=github&utm_medium=link&utm_campaign=signup#)。
2. **连接数据库。** 按照适用于您环境的[安装指南](https://docs.releem.com/installation)安装 Releem Agent。
3. **检查连接。** 确认 Agent 状态为 Connected，且控制台正在接收当前指标。
4. **审核并应用建议。** 审核查询、索引和配置建议，并在准备好后应用受支持的变更。

有关托管服务，请查看[当前套餐](https://releem.com/pricing)。

<a name="security"></a>

## 🔒 安全

根据数据库和已启用的功能，收集的信息可能包括系统资源统计、数据库设置和状态、数据库模式名称与表名、结构和索引、查询执行统计、查询示例以及 EXPLAIN 计划。

连接前，请检查数据库和已启用功能所需的权限。有关 Agent 数据收集和传输的信息，请阅读[数据安全常见问题](https://releem.com/faq)，并参阅下方的兼容性文档，了解各数据库引擎的具体要求。

## 🗄️ 兼容性

### 数据库版本

| 数据库 | 文档列出的受支持版本 |
| --- | --- |
| MySQL | 5.5, 5.6, 5.7, 8.0, 8.4, 9 |
| MariaDB | 10, 11, 12, 13 |
| PostgreSQL | 12, 13, 14, 15, 16, 17, 18 |
| Percona Server for MySQL | 5.7, 8.0, 8.4, 9.7 |

请查阅[受支持的数据库、版本和权限](https://docs.releem.com/supported-databases)，了解当前要求。

### 受支持的环境

- **Linux:** CentOS、CloudLinux、Debian、Ubuntu 和 Rocky Linux。
- **Windows Server:** 2012、2016、2019、2022 和 2025。
- **容器与编排:** Docker 和 Kubernetes。
- **Amazon RDS:** MySQL、MariaDB 和 PostgreSQL。
- **Amazon Aurora:** MySQL 和 PostgreSQL。
- **Google Cloud SQL:** MySQL 和 PostgreSQL。
- **Azure Database for MySQL.**
- **托管与集群:** MySQL/MariaDB 集群和 WHM/cPanel。

选择 [MySQL](https://docs.releem.com/installation/mysql)、[MariaDB](https://docs.releem.com/installation/mariadb) 或 [PostgreSQL](https://docs.releem.com/installation/postgresql) 的安装方式。可用环境、功能和自动化操作取决于数据库、版本、部署方式、权限和套餐。

## 🧪 性能测试

这些历史文章介绍了特定的基准测试环境和配置比较。其结果适用于文档说明的测试条件。

- [MySQL 5.7 配置基准测试](https://releem.com/blog/how-to-improve-performance-mysql57-default-configuration)
- [MySQL 8 配置基准测试](https://releem.com/blog/mysql-8-performance-benchmark)
- [MySQL 配置如何影响 Web 应用](https://releem.com/blog/web-applications-performance)

## 🤝 支持、反馈与路线图

此仓库是整个 Releem 产品的公开反馈、问题报告和路线图讨论平台，涵盖控制台、建议、集成和 Agent。

- **🤝 获取帮助:** 加入 [Releem Slack 社区](https://join.slack.com/t/releem-community/shared_invite/zt-1j3d0vosh-AJHbDiQrzVDvLat5eqQorQ)，提出问题、参与社区讨论或获取配置帮助。
- **🐛 报告缺陷:** 报告问题前，请先搜索 [Issues](https://github.com/Releem/releem-agent/issues)。请说明受影响的组件、数据库版本、部署方式和复现步骤。
- **💡 提出改进建议并讨论路线图优先级:** 通过 [Discussions](https://github.com/Releem/releem-agent/discussions) 说明您的使用场景和期望结果。

请从公开报告中移除凭据、个人信息，以及查询或数据库结构中的敏感细节。

通过 [X](https://twitter.com/releemhq) 和 [LinkedIn](https://www.linkedin.com/company/releem) 关注产品动态。

## 🧩 参与贡献

欢迎针对整个 Releem 产品提供反馈和可复现的缺陷报告。本仓库的代码贡献适用于 Agent。请说明问题、拟议变更及验证方法。有关已发布代码的条款，请参阅仓库[许可证](../../LICENSE)。
