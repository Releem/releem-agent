<h1 align="center"><a href="https://releem.com/"><img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem-icon-top.png" width="24" height="24" alt="Logotipo do Releem" /></a> Releem</h1>

**Ferramenta de otimização de desempenho do MySQL com IA e automação controlada**

O Releem é uma ferramenta de otimização de desempenho do MySQL com IA e automação controlada, que atua como seu assistente de banco de dados desde a primeira decisão de otimização.

Para engenheiros, DBAs e equipes de infraestrutura responsáveis pelo desempenho do MySQL em produção. Melhore o desempenho das consultas e a eficiência no uso de recursos com recomendações baseadas na carga de trabalho. Reavalie o que precisa de atenção conforme as consultas, o tráfego e os dados mudam.

<p align="center">
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/MySQL-4479A1?logo=mysql&amp;logoColor=white" alt="MySQL" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/MariaDB-003545?logo=mariadb&amp;logoColor=white" alt="MariaDB" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/PostgreSQL-336791?logo=postgresql&amp;logoColor=white" alt="PostgreSQL" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/Percona_Server_for_MySQL-orange" alt="Percona Server for MySQL" /></a>
  <br />
  <a href="https://hub.docker.com/r/releem/releem-agent"><img src="https://img.shields.io/docker/pulls/releem/releem-agent?logo=docker&amp;logoColor=white" alt="Downloads no Docker" /></a>
    <a href="https://docs.releem.com/installation"><img src="https://img.shields.io/badge/platforms-Linux%20%7C%20Windows%20%7C%20Docker%20%7C%20K8s%20%7C%20AWS%20%7C%20GCP%20%7C%20Azure%20%7C%20WHM%2FcPanel%20%7C%20Clusters-555555" alt="Plataformas: Linux, Windows, Docker, Kubernetes, Amazon RDS, Amazon Aurora, Google Cloud SQL, Azure Database for MySQL, WHM/cPanel e clusters" /></a>
  <a href="https://github.com/Releem/releem-agent/stargazers"><img src="https://img.shields.io/github/stars/Releem/releem-agent?style=flat&amp;logo=github" alt="Estrelas no GitHub" /></a>
  <a href="https://github.com/Releem/releem-agent"><img src="https://img.shields.io/badge/Agent-open_source-2ea44f?logo=github" alt="Agent de código aberto" /></a>
  <a href="https://join.slack.com/t/releem-community/shared_invite/zt-1j3d0vosh-AJHbDiQrzVDvLat5eqQorQ"><img src="https://img.shields.io/badge/Slack-Join_community-4A154B?logo=slack&amp;logoColor=white" alt="Comunidade no Slack" /></a>
</p>

<p align="center">
  <a href="../../README.md">English</a> •
  <a href="README.ru.md">Русский</a> •
  <a href="README.es.md">Español</a> •
  <b>Português</b> •
  <a href="README.zh.md">简体中文</a> •
  <a href="README.fr.md">Français</a> •
  <a href="README.id.md">Bahasa Indonesia</a>
</p>

<p align="center">
  <a href="#getting-started">Comece agora</a> |
  <a href="https://docs.releem.com/">Documentação</a> |
  <a href="https://releem.com/mysql-configuration-tuning">Ajuste da configuração do MySQL</a> |
  <a href="https://releem.com/sql-query-optimization">Otimização de consultas SQL</a> |
  <a href="https://releem.com/compare/mysqltuner">Compare com o MySQLTuner</a> |
  <a href="#security">Segurança</a>
</p>

Este repositório contém o **Releem Agent** de código aberto, que conecta seu banco de dados ao serviço de assinatura hospedado do Releem. Ele também reúne feedback, Issues e discussões sobre o roadmap de todo o produto Releem.

<p align="center">
  <img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem_dashboard.png" width="90%" alt="Painel do Releem" />
</p>

## ⚡ Por que usar o Releem?

- **[Otimização de consultas e índices](https://releem.com/sql-query-optimization):** Analise recomendações de consultas e [índices](https://docs.releem.com/recommendations) baseadas na carga de trabalho para melhorar o desempenho das consultas MySQL.
- **[Ajuste da configuração do MySQL](https://releem.com/mysql-configuration-tuning):** Analise configurações baseadas na carga de trabalho, com ajustes aprimorados por aprendizado de máquina, para melhorar a eficiência no uso de recursos.
- **Automação controlada:** Aplique alterações de configuração compatíveis e alterações de esquema aprovadas e elegíveis pelos fluxos documentados. Sua equipe mantém o controle sobre o que muda e quando.
- **[Monitoramento e verificações de integridade](https://releem.com/health-checks):** Use métricas do banco de dados e verificações de integridade para identificar o que precisa de atenção e comparar o desempenho observado e o uso de recursos após as alterações.

Consulte o [processo de ajuste da configuração](https://docs.releem.com/recommendations/configuration-tuning/mysql-tuning-process) e o [fluxo de execução de alterações de esquema](https://docs.releem.com/recommendations/query-optimization/automatic-schema-changes) para conhecer as alterações compatíveis e as etapas de aplicação.

<p align="center">
  <strong>Encontre e aplique os índices MySQL que faltam com o Releem</strong><br /><br />
  <a href="https://www.youtube.com/watch?v=Pd-DUK0so9Y">
    <img src="https://img.youtube.com/vi/Pd-DUK0so9Y/hqdefault.jpg" width="480" alt="Veja como encontrar e aplicar os índices MySQL que faltam com o Releem" />
  </a>
  <br />
  <a href="https://www.youtube.com/watch?v=Pd-DUK0so9Y">▶ Assista: encontre e aplique os índices MySQL que faltam com o Releem</a>
</p>

<p align="center">
  <strong>Aplique uma configuração MySQL recomendada</strong><br /><br />
  <a href="https://youtu.be/XqBZPGRlrGs">
    <img src="https://raw.githubusercontent.com/Releem/docs/main/assets/images/applying-conf-dark.gif"
         width="80%"
         alt="Painel do Releem mostrando como analisar a justificativa, verificar se é necessário reiniciar e aplicar uma configuração MySQL recomendada" />
  </a>
  <br />
  <a href="https://youtu.be/XqBZPGRlrGs">▶ Assista à demonstração de 15 segundos no YouTube</a>
</p>

<p align="center">
  <strong>Aplique uma configuração pela CLI do Releem Agent</strong><br /><br />
  <img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem-applying.gif" width="80%" alt="Aplicação de uma configuração com o Releem Agent" />
</p>

## 🧭 Como funciona

1. **Analisa a carga de trabalho do MySQL.** Coleta a atividade das consultas, as configurações do banco de dados e as métricas de recursos para entender como seu banco de dados funciona.
2. **Identifica oportunidades de otimização de desempenho.** Destaca consultas lentas ou executadas com frequência e constatações de configuração que podem afetar o desempenho.
3. **Ajuda a definir prioridades.** Usa observações da carga de trabalho e as constatações para ajudar você a escolher o que tratar primeiro.
4. **Recomenda alterações em consultas, índices e configurações.** Apresenta melhorias propostas com explicações para sua equipe avaliar.
5. **Ajuda a aplicar alterações compatíveis.** Aplique alterações de configuração compatíveis e alterações de esquema aprovadas por meio de automação controlada.
6. **Ajuda a medir o impacto.** Compare o desempenho das consultas, o uso de recursos e as configurações efetivas após uma alteração.

<a name="getting-started"></a>

## 🚀 Primeiros passos com o Releem

1. **Crie sua conta.** [Cadastre-se no Releem](https://releem.com/?utm_source=github&utm_medium=link&utm_campaign=signup#).
2. **Conecte seu banco de dados.** Instale o Releem Agent usando o [guia de instalação](https://docs.releem.com/installation) adequado ao seu ambiente.
3. **Verifique a conexão.** Confirme que o status do Agent é Connected e que as métricas atuais estão chegando ao painel.
4. **Analise e aplique as recomendações.** Analise as recomendações de consultas, índices e configurações. Depois, aplique as alterações compatíveis quando estiver pronto.

Consulte os [planos atuais](https://releem.com/pricing) do serviço hospedado.

<a name="security"></a>

## 🔒 Segurança

Dependendo do banco de dados e dos recursos habilitados, as informações coletadas podem incluir estatísticas de recursos do sistema, configurações e status do banco de dados, nomes de esquemas e tabelas, estrutura e índices, estatísticas de execução de consultas, exemplos de consultas e planos EXPLAIN.

Antes de conectar, confira os privilégios exigidos pelo seu banco de dados e pelos recursos habilitados. Leia as [perguntas frequentes sobre segurança dos dados](https://releem.com/faq) para saber mais sobre a coleta e o transporte de dados pelo Agent, além da documentação de compatibilidade abaixo para os requisitos específicos de cada mecanismo.

## 🗄️ Compatibilidade

### Versões de banco de dados

| Banco de dados | Versões compatíveis documentadas |
| --- | --- |
| MySQL | 5.5, 5.6, 5.7, 8.0, 8.4, 9 |
| MariaDB | 10, 11, 12, 13 |
| PostgreSQL | 12, 13, 14, 15, 16, 17, 18 |
| Percona Server for MySQL | 5.7, 8.0, 8.4, 9.7 |

Consulte os [bancos de dados, versões e permissões compatíveis](https://docs.releem.com/supported-databases) para verificar os requisitos atuais.

### Ambientes compatíveis

- **Linux:** CentOS, CloudLinux, Debian, Ubuntu e Rocky Linux.
- **Windows Server:** 2012, 2016, 2019, 2022 e 2025.
- **Contêineres e orquestração:** Docker e Kubernetes.
- **Amazon RDS:** MySQL, MariaDB e PostgreSQL.
- **Amazon Aurora:** MySQL e PostgreSQL.
- **Google Cloud SQL:** MySQL e PostgreSQL.
- **Azure Database for MySQL.**
- **Hospedagem e clusters:** Clusters MySQL/MariaDB e WHM/cPanel.

Escolha o procedimento de instalação para [MySQL](https://docs.releem.com/installation/mysql), [MariaDB](https://docs.releem.com/installation/mariadb) ou [PostgreSQL](https://docs.releem.com/installation/postgresql). As opções de ambiente, os recursos e as ações automatizadas dependem do banco de dados, da versão, da implantação, das permissões e do plano.

## 🧪 Testes de desempenho

Estes artigos históricos descrevem ambientes específicos de benchmark e comparações de configuração. Os resultados se aplicam às condições de teste documentadas.

- [Benchmark de configuração do MySQL 5.7](https://releem.com/blog/how-to-improve-performance-mysql57-default-configuration)
- [Benchmark de configuração do MySQL 8](https://releem.com/blog/mysql-8-performance-benchmark)
- [Como a configuração do MySQL afeta as aplicações web](https://releem.com/blog/web-applications-performance)

## 🤝 Suporte, feedback e roadmap

Este repositório é o espaço público para feedback, relatos de problemas e discussões sobre o roadmap de todo o produto Releem, incluindo o painel, as recomendações, as integrações e o Agent.

- **🤝 Obtenha ajuda:** Participe da [Comunidade Releem no Slack](https://join.slack.com/t/releem-community/shared_invite/zt-1j3d0vosh-AJHbDiQrzVDvLat5eqQorQ) para tirar dúvidas, conversar com a comunidade e obter ajuda na configuração.
- **🐛 Relate um bug:** Pesquise nas [Issues](https://github.com/Releem/releem-agent/issues) antes de relatar um problema. Informe o componente afetado, a versão do banco de dados, a implantação e as etapas para reproduzir o problema.
- **💡 Sugira melhorias e discuta as prioridades do roadmap:** Use as [Discussions](https://github.com/Releem/releem-agent/discussions) para explicar seu caso de uso e o resultado de que precisa.

Remova credenciais, informações pessoais e detalhes sensíveis de consultas ou esquemas dos relatos públicos.

Acompanhe as novidades do produto no [X](https://twitter.com/releemhq) e no [LinkedIn](https://www.linkedin.com/company/releem).

## 🧩 Contribua

Feedback sobre o produto e relatos de bugs reproduzíveis são bem-vindos em todo o Releem. As contribuições de código neste repositório se aplicam ao Agent. Descreva o problema, a alteração proposta e como você a verificou. Consulte a [licença](../../LICENSE) do repositório para conhecer os termos do código publicado.
