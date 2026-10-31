<h1 align="center"><a href="https://releem.com/"><img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem-icon-top.png" width="24" height="24" alt="Releem logo" /></a> Releem</h1>

**MySQL performance tuning tool with AI and controlled automation**

Releem is a MySQL performance tuning tool with AI and controlled automation, serving as your Database Advisor from the first tuning decision onward.

For engineers, DBAs and infrastructure teams responsible for production MySQL performance. Improve query performance and resource efficiency with workload-based recommendations, and review what needs attention as queries, traffic and data change.

<p align="center">
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/MySQL-4479A1?logo=mysql&amp;logoColor=white" alt="MySQL" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/MariaDB-003545?logo=mariadb&amp;logoColor=white" alt="MariaDB" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/PostgreSQL-336791?logo=postgresql&amp;logoColor=white" alt="PostgreSQL" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/Percona_Server_for_MySQL-orange" alt="Percona Server for MySQL" /></a>
  <br />
  <a href="https://hub.docker.com/r/releem/releem-agent"><img src="https://img.shields.io/docker/pulls/releem/releem-agent?logo=docker&amp;logoColor=white" alt="Docker pulls" /></a>
    <a href="https://docs.releem.com/installation"><img src="https://img.shields.io/badge/platforms-Linux%20%7C%20Windows%20%7C%20Docker%20%7C%20K8s%20%7C%20AWS%20%7C%20GCP%20%7C%20Azure%20%7C%20WHM%2FcPanel%20%7C%20Clusters-555555" alt="Platforms: Linux, Windows, Docker, Kubernetes, Amazon RDS, Amazon Aurora, Google Cloud SQL, Azure Database for MySQL, WHM/cPanel and clusters" /></a>
  <a href="https://github.com/Releem/releem-agent/stargazers"><img src="https://img.shields.io/github/stars/Releem/releem-agent?style=flat&amp;logo=github" alt="GitHub stars" /></a>
  <a href="https://github.com/Releem/releem-agent"><img src="https://img.shields.io/badge/Agent-open_source-2ea44f?logo=github" alt="Open-source Agent" /></a>
  <a href="https://join.slack.com/t/releem-community/shared_invite/zt-1j3d0vosh-AJHbDiQrzVDvLat5eqQorQ"><img src="https://img.shields.io/badge/Slack-Join_community-4A154B?logo=slack&amp;logoColor=white" alt="Slack community" /></a>
</p>

<p align="center">
  <a href="#getting-started">Get started</a> |
  <a href="https://docs.releem.com/">Docs</a> |
  <a href="https://releem.com/mysql-configuration-tuning">MySQL configuration tuning</a> |
  <a href="https://releem.com/sql-query-optimization">SQL query optimization</a> |
  <a href="https://releem.com/compare/mysqltuner">Compare MySQLTuner</a> |
  <a href="#security">Security</a>
</p>

This repository contains the open-source **Releem Agent**, which connects your database to the hosted Releem subscription service. It also hosts feedback, Issues and roadmap discussions for the whole Releem product.

<p align="center">
  <img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem_dashboard.png" width="90%" alt="Releem dashboard" />
</p>

## ⚡ Why Releem?

- **[Query and index optimization](https://releem.com/sql-query-optimization):** Review workload-based query and [index recommendations](https://docs.releem.com/recommendations) to improve MySQL query performance.
- **[MySQL configuration tuning](https://releem.com/mysql-configuration-tuning):** Review workload-based settings, with tuning enhanced by machine learning, to improve resource efficiency.
- **Controlled automation:** Apply supported configuration changes and eligible approved schema changes through documented workflows, keeping your team in control of what changes and when.
- **[Monitoring and health checks](https://releem.com/health-checks):** Use database metrics and health checks to identify what needs attention and compare observed performance and resource use after changes.

See the [configuration tuning process](https://docs.releem.com/recommendations/configuration-tuning/mysql-tuning-process) and [schema execution workflow](https://docs.releem.com/recommendations/query-optimization/automatic-schema-changes) for supported changes and application steps.

<p align="center">
  <img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem-applying-click.gif" width="80%" alt="Applying a configuration through Releem" />
</p>

<p align="center">
  <img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem-applying.gif" width="80%" alt="Applying a configuration with the Releem Agent" />
</p>

## 🧭 How it works

1. **Analyzes your MySQL workload.** Collects query activity, database settings and resource metrics to understand how your database runs.
2. **Identifies performance tuning opportunities.** Surfaces slow or frequently executed queries and configuration findings that may affect performance.
3. **Helps prioritize what to address.** Uses workload observations and findings to help you choose what to address first.
4. **Recommends query, index and configuration changes.** Presents proposed improvements with explanations for your team to review.
5. **Helps apply supported changes.** Apply supported configuration and approved schema changes through controlled automation.
6. **Helps you measure the impact.** Compare query performance, resource use and effective settings after a change.

<a name="getting-started"></a>

## 🚀 Getting started with Releem

1. **Create your account.** [Sign up for Releem](https://releem.com/?utm_source=github&utm_medium=link&utm_campaign=signup#).
2. **Connect your MySQL database.** Install the Releem Agent using the [installation guide](https://docs.releem.com/installation) for your environment.
3. **Check the connection.** Confirm the Agent is connected and current metrics are arriving in the dashboard.
4. **Review and apply recommendations.** Review query, index and configuration recommendations, then apply supported changes when you are ready.

See [current plans](https://releem.com/pricing) for the hosted service.

<a name="security"></a>

## 🔒 Security

Depending on the database and enabled features, collected information can include system resource statistics, database settings and status, schema and table names, structure and indexes, query execution statistics, query examples and EXPLAIN plans.

Review the required privileges for your database and enabled features before connecting. Read the [data-security FAQ](https://releem.com/faq) for information about Agent data collection and transport, and the compatibility documentation below for engine-specific requirements.

## 🗄️ Compatibility

### Database versions

| Database | Documented supported versions |
| --- | --- |
| MySQL | 5.5, 5.6, 5.7, 8.0, 8.4, 9 |
| MariaDB | 10, 11, 12, 13 |
| PostgreSQL | 12, 13, 14, 15, 16, 17, 18 |
| Percona Server for MySQL | 5.7, 8.0, 8.4, 9.7 |

Check the [supported databases, versions and permissions](https://docs.releem.com/supported-databases) for current requirements.

### Supported environments

- **Linux:** CentOS, CloudLinux, Debian, Ubuntu and Rocky Linux.
- **Windows Server:** 2012, 2016, 2019, 2022 and 2025.
- **Containers and orchestration:** Docker and Kubernetes.
- **Amazon RDS:** MySQL, MariaDB and PostgreSQL.
- **Amazon Aurora:** MySQL and PostgreSQL.
- **Google Cloud SQL:** MySQL and PostgreSQL.
- **Azure Database for MySQL.**
- **Hosting and clusters:** MySQL/MariaDB clusters and WHM/cPanel.

Choose the installation path for [MySQL](https://docs.releem.com/installation/mysql), [MariaDB](https://docs.releem.com/installation/mariadb) or [PostgreSQL](https://docs.releem.com/installation/postgresql). Environment options, features and automated actions depend on the database, version, deployment, permissions and plan. The list above does not imply that every database is available in every environment.

## 🧪 Performance tests

These historical articles describe specific benchmark environments and configuration comparisons. Their results apply to the documented test conditions.

- [MySQL 5.7 configuration benchmark](https://releem.com/blog/how-to-improve-performance-mysql57-default-configuration)
- [MySQL 8 configuration benchmark](https://releem.com/blog/mysql-8-performance-benchmark)
- [How MySQL configuration affects web applications](https://releem.com/blog/web-applications-performance)

## 🤝 Support, feedback and roadmap

This repository is the public home for feedback, issues and roadmap discussion across the whole Releem product, including the dashboard, recommendations, integrations and Agent.

- **🤝 Get help:** Join the [Releem Community on Slack](https://join.slack.com/t/releem-community/shared_invite/zt-1j3d0vosh-AJHbDiQrzVDvLat5eqQorQ) for questions, community discussion and setup help.
- **🐛 Report a bug:** Search [Issues](https://github.com/Releem/releem-agent/issues) before reporting a problem. Include the affected component, database version, deployment and reproduction steps.
- **💡 Suggest improvements and discuss roadmap priorities:** Use [Discussions](https://github.com/Releem/releem-agent/discussions) to explain your use case and the result you need.

Remove credentials, personal information and sensitive query or schema details from public reports.

Follow product updates on [X](https://twitter.com/releemhq) and [LinkedIn](https://www.linkedin.com/company/releem).

## 🧩 Contribute

Product feedback and reproducible bug reports are welcome across Releem. Code contributions in this repository apply to the Agent. Describe the problem, proposed change and how you verified it. See the repository [license](LICENSE) for the published code's terms.
