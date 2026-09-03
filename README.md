# Releem Agent

[![Go Report Card](https://goreportcard.com/badge/github.com/Releem/mysqlconfigurer)](https://goreportcard.com/report/github.com/Releem/mysqlconfigurer)
[![slack](https://img.shields.io/badge/slack-Releem%20Community-brightgreen.svg?logo=slack)](https://join.slack.com/t/releem-community/shared_invite/zt-1j3d0vosh-AJHbDiQrzVDvLat5eqQorQ)

<p align="center">
  <a href="https://releem.com/">
    <img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem-icon-top.png" width="140px" alt="Releem" />
  </a>
</p>
<p align="center">
    <a href="https://docs.releem.com">Docs</a> | 
    <a href="#security">Security</a> |
    <a href="https://releem.com/compare/mysqltuner">Compare MySQLTuner</a> |
    <a href="https://releem.com/sql-query-optimization">SQL Query Optimization</a> |
    <a href="https://releem.com/mysql-optimization-center">MySQL Optimization Center</a> |
    <a href="https://releem.com/blog">Blog</a>
    <br /><br />
</p>



The present repository contains the source code of the **Releem Agent**.

[Releem](https://releem.com) is a database advisor for MySQL, MariaDB & PostgreSQL that provides automatic metrics analysis, actionable insights, and safe automation. It helps detect issues quickly, optimize performance continuously, and reduce manual work at scale.

With Releem we are trying to bring top-notch experience in database management and save thousands of software engineers hours.

<p align="center">
<img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem_dashboard.png" width="90%">
</p>

## Why Releem?
- **Clutter Free**: Releem provides simple dashboard and it cuts through the noise. No layers of menus, no need for custom reports. Get all the important metrics on one single page. No training necessary.
- **Hassle free**: Simple one-step Installation on most popular Linux platforms and Support of all MySQL/MariaDB/Percona versions.
- **Performance Booster**: Recommended configuration delivers up to [290% boost](#Tests) to MySQL performance compare to the default configuration.
- **Simplified Monitoring**: [MySQL Health Checks](https://releem.com/blog/mysql-health-checks?utm_source=github&utm_medium=social&utm_campaign=mysql-health-checks&utm_content=post) greatly simplifies the process of monitoring and maintaining a healthy database by focusing on key aspects that describe the efficiency and "best practices" of using Memory, Connections, Logs, Cache, Disk, Indexes, and Threads. Releem Score metric calculates by summarizing Health Checks statuses.
- **Automatic SQL Query Optimization and Index suggestions**: Releem automatically identifies inefficient queries and offers missed indexes, enabling database administrators to boost query performance without extensive manual analysis.
- **Security**: Releem Agent is open-source and does not collect your database data. [Learn more](#security)
- **Email report**: Keep an eye on your servers with weekly email reports.
- **Simple Applying**: Releem Agent allows simply apply recommended MySQL configuration just in one click or in one command.
<p align="center">
<img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem-applying-click.gif" width="80%">
</p>

<p align="center">
<img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem-applying.gif" width="80%">
</p>

## How it works

**Releem Agent** - Has been installed on servers, collects MySQL metrics, sends them to Cloud Platforms, and applies MySQL configurations. Open Source daemon built on Go.

**Releem Cloud Platform** - Analyzes collected metrics, detects performance issues, and recommends MySQL configurations.

**Releem Customer Portal** - Web interface displays recommended configurations and current information about all MySQL servers with installed Releem Agent. It looks like this on the screenshot.

## Getting started with Releem
The easiest way to get started with Releem is with [our managed service in the cloud](https://releem.com) and one step installation command. It takes up to 5 minutes to start monitoring your MySQL servers and get recommendations to improve performance.

To start using Releem just sign up at [https://releem.com](https://releem.com/?utm_source=github&utm_medium=link&utm_campaign=signup#) and install Releem Agent on your server.

### Aurora parameter groups

Releem supports Amazon Aurora MySQL and Aurora PostgreSQL. Configure one Agent
for each DB instance endpoint. Aurora onboarding requires both a custom DB
parameter group for the instance and a custom DB cluster parameter group for
the cluster. Their configured names must match the groups attached in AWS, and
both groups must be ready before the Agent applies recommendations. This is
required because a recommendation can target either parameter group. Only the
Agent that targets the writer modifies cluster parameters, so it must have
`rds:ModifyDBClusterParameterGroup` permission.

For a non-Aurora RDS instance, only the DB parameter group is required; leave
`DBClusterParameterGroup` empty. AWS-managed default groups cannot be modified
and are not valid Aurora onboarding targets.

`DatabaseType` defaults to `mysql`, preserving existing RDS and Aurora MySQL
stacks. Select `postgresql` for Aurora PostgreSQL. The same `DBUser`,
`DBPassword`/`DBPasswordArn`, and `DBSSLMode` inputs are then exposed to the
container as PostgreSQL (`PG_*`) settings so the Agent starts its PostgreSQL
collector; the default `mysql` selection continues exposing the existing
MySQL (`DB_*`) settings.

## Security

Releem does not collect any user data.

The Releem agent is open-source and does not require opening ports.

The Releem agent collects the following data:
- Memory, CPU, and disk usage statistics
- MySQL system variables & status information
- Data size statistics from information_schema
- Table & schema names (but not actual table content)
- Table structure details, indexes, and usage statistics for schema optimization
- Query execution statistics with placeholders from performance_schema, including execution counts, average execution time, query example, and EXPLAIN plans for the top queries

## Support
Join the Releem Community on [Slack](https://join.slack.com/t/releem-community/shared_invite/zt-1j3d0vosh-AJHbDiQrzVDvLat5eqQorQ). 

## Compatibility
- MySQL 8.0, MySQL 5.7, MySQL 5.6, MySQL 5.5
- MariaDB 10.1, MariaDB 10.2, MariaDB 10.3, MariaDB 10.4, MariaDB 10.5, MariaDB 10.6, MariaDB 10.7, MariaDB 10.8, MariaDB 10.9, MariaDB 10.10, MariaDB 10.11, MariaDB 11.0
- Percona 8.0, Percona 5.7, Percona 5.6, Percona 5.5
- PostgreSQL 15, PostgreSQL 16, PostgreSQL 17, PostgreSQL 18
- Centos, CloudLinux, Debian, Ubuntu, RockyLinux
- Windows Server 2012, Windows Server 2016, Windows Server 2019, Windows Server 2022, Windows Server 2025
- Amazon RDS MySQL, Amazon RDS Aurora MySQL, Amazon RDS Aurora PostgreSQL, Amazon RDS MariaDB
- Google Cloud SQL MySQL, Google Cloud SQL PostgreSQL

*** MINIMAL REQUIREMENTS ***
- Unix/Linux based operating system (tested on Linux, BSD variants, and Solaris variants)
- Unrestricted read access to the MySQL server

## Tests
We tested the results with Sysbench on a virtual server running Debian 9 (2 CPU, 2GB Ram) the table contained 10 million entries.
Two configurations were tested, the MySQL default configuration and the configuration recommended by the **Releem** service. The tests were two-step: read (test1) only and read/write (test2).

Recommended configuration delivered a 30% boost to MySQL performance compared to the default configuration. 

Follow this links to see results:
- [MySQL 5.7 Benchmark](https://releem.com/blog/how-to-improve-performance-mysql57-default-configuration)
- [MySQL 8 Benchmark](https://releem.com/blog/mysql-8-performance-benchmark)
- [How MySQL Configuration Impacts the Performance of Web Applications](https://releem.com/blog/web-applications-performance)

## Feedback 
We welcome feedback from our community. Take a look at our [feedback board](https://releem.com/wall-of-love). Please let us know if you have any requests and vote on open issues so we can better prioritize.

To stay up to date with all the latest news and product updates, make sure to follow us on [Twitter](https://twitter.com/releemhq), [LinkedIn](https://www.linkedin.com/company/releem).

## Contribute

You can help us by reporting problems, suggestions or contributing to the code.

### Report a problem or suggestion

Go to our [issue tracker](https://github.com/releem/mysqlconfigurer/issues) and check if your problem is already reported. If not, create a new issue with a descriptive title and detail your suggestion or steps to reproduce the problem.

If you have suggestions or want to discuss potential improvements, please visit our [Discussions](https://github.com/releem/mysqlconfigurer/discussions) page. We value your input and look forward to engaging with the community to enhance our product.
