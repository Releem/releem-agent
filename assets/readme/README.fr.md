<h1 align="center"><a href="https://releem.com/"><img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem-icon-top.png" width="24" height="24" alt="Logo Releem" /></a> Releem</h1>

**Outil d’optimisation des performances MySQL avec IA et automatisation contrôlée**

Releem est un outil d’optimisation des performances MySQL avec IA et automatisation contrôlée, qui vous accompagne comme conseiller en bases de données dès la première décision d’optimisation.

Pour les ingénieurs, les administrateurs de bases de données et les équipes d’infrastructure responsables des performances de MySQL en production. Améliorez les performances des requêtes et utilisez les ressources plus efficacement grâce à des recommandations fondées sur la charge de travail. Identifiez les points à examiner lorsque les requêtes, le trafic et les données évoluent.

<p align="center">
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/MySQL-4479A1?logo=mysql&amp;logoColor=white" alt="MySQL" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/MariaDB-003545?logo=mariadb&amp;logoColor=white" alt="MariaDB" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/PostgreSQL-336791?logo=postgresql&amp;logoColor=white" alt="PostgreSQL" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/Percona_Server_for_MySQL-orange" alt="Percona Server for MySQL" /></a>
  <br />
  <a href="https://hub.docker.com/r/releem/releem-agent"><img src="https://img.shields.io/docker/pulls/releem/releem-agent?logo=docker&amp;logoColor=white" alt="Téléchargements Docker" /></a>
    <a href="https://docs.releem.com/installation"><img src="https://img.shields.io/badge/platforms-Linux%20%7C%20Windows%20%7C%20Docker%20%7C%20K8s%20%7C%20AWS%20%7C%20GCP%20%7C%20Azure%20%7C%20WHM%2FcPanel%20%7C%20Clusters-555555" alt="Plateformes : Linux, Windows, Docker, Kubernetes, Amazon RDS, Amazon Aurora, Google Cloud SQL, Azure Database for MySQL, WHM/cPanel et clusters" /></a>
  <a href="https://github.com/Releem/releem-agent/stargazers"><img src="https://img.shields.io/github/stars/Releem/releem-agent?style=flat&amp;logo=github" alt="Étoiles GitHub" /></a>
  <a href="https://github.com/Releem/releem-agent"><img src="https://img.shields.io/badge/Agent-open_source-2ea44f?logo=github" alt="Agent open source" /></a>
  <a href="https://join.slack.com/t/releem-community/shared_invite/zt-1j3d0vosh-AJHbDiQrzVDvLat5eqQorQ"><img src="https://img.shields.io/badge/Slack-Join_community-4A154B?logo=slack&amp;logoColor=white" alt="Communauté Slack" /></a>
</p>

<p align="center">
  <a href="../../README.md">English</a> •
  <a href="README.ru.md">Русский</a> •
  <a href="README.es.md">Español</a> •
  <a href="README.pt.md">Português</a> •
  <a href="README.zh.md">简体中文</a> •
  <b>Français</b> •
  <a href="README.id.md">Bahasa Indonesia</a>
</p>

<p align="center">
  <a href="#getting-started">Premiers pas</a> |
  <a href="https://docs.releem.com/">Documentation</a> |
  <a href="https://releem.com/mysql-configuration-tuning">Réglage de la configuration MySQL</a> |
  <a href="https://releem.com/sql-query-optimization">Optimisation des requêtes SQL</a> |
  <a href="https://releem.com/compare/mysqltuner">Comparaison avec MySQLTuner</a> |
  <a href="#security">Sécurité</a>
</p>

Ce dépôt contient **Releem Agent**, l’agent open source qui connecte votre base de données au service hébergé Releem par abonnement. Il accueille également les retours, les Issues et les discussions sur la feuille de route de l’ensemble du produit Releem.

<p align="center">
  <img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem_dashboard.png" width="90%" alt="Tableau de bord Releem" />
</p>

## ⚡ Pourquoi Releem ?

- **[Optimisation des requêtes et des index](https://releem.com/sql-query-optimization):** Examinez les recommandations fondées sur la charge de travail concernant les requêtes et les [index](https://docs.releem.com/recommendations) pour améliorer les performances des requêtes MySQL.
- **[Réglage de la configuration MySQL](https://releem.com/mysql-configuration-tuning):** Examinez les recommandations de configuration fondées sur la charge de travail. L’apprentissage automatique aide à affiner les réglages pour utiliser les ressources plus efficacement.
- **Automatisation contrôlée:** Appliquez les modifications de configuration recommandées et les modifications de schéma approuvées. Votre équipe garde le contrôle sur les changements et leur calendrier.
- **[Surveillance et contrôles d’état](https://releem.com/health-checks):** Utilisez les métriques de la base de données et les contrôles d’état pour identifier les points à examiner et comparer les performances observées et l’utilisation des ressources après les changements.

Consultez le [processus de réglage de la configuration](https://docs.releem.com/recommendations/configuration-tuning/mysql-tuning-process) et la [procédure d’application des modifications de schéma](https://docs.releem.com/recommendations/query-optimization/automatic-schema-changes) pour connaître les étapes d’application des configurations et des modifications de schéma approuvées.

<p align="center">
  <strong>Identifiez et ajoutez les index manquants dans MySQL avec Releem</strong><br /><br />
  <a href="https://www.youtube.com/watch?v=Pd-DUK0so9Y">
    <img src="https://img.youtube.com/vi/Pd-DUK0so9Y/hqdefault.jpg" width="480" alt="Découvrez comment identifier et ajouter les index manquants dans MySQL avec Releem" />
  </a>
  <br />
  <a href="https://www.youtube.com/watch?v=Pd-DUK0so9Y">▶ Voir : Identifiez et ajoutez les index manquants dans MySQL avec Releem</a>
</p>

<p align="center">
  <strong>Appliquez une configuration MySQL recommandée</strong><br /><br />
  <a href="https://youtu.be/XqBZPGRlrGs">
    <img src="https://raw.githubusercontent.com/Releem/docs/main/assets/images/applying-conf-dark.gif"
         width="80%"
         alt="Tableau de bord Releem montrant comment examiner la justification, vérifier si un redémarrage est nécessaire et appliquer une configuration MySQL recommandée" />
  </a>
  <br />
  <a href="https://youtu.be/XqBZPGRlrGs">▶ Voir la démonstration de 15 secondes sur YouTube</a>
</p>

<p align="center">
  <strong>Appliquez une configuration avec la CLI de Releem Agent</strong><br /><br />
  <img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem-applying.gif" width="80%" alt="Application d’une configuration avec Releem Agent" />
</p>

## 🧭 Fonctionnement

1. **Releem analyse votre charge de travail MySQL.** Il collecte l’activité des requêtes, les paramètres de la base de données et les métriques des ressources pour comprendre son fonctionnement.
2. **Releem identifie les possibilités d’optimisation des performances.** Il met en évidence les requêtes lentes ou fréquemment exécutées et les points de configuration susceptibles d’affecter les performances.
3. **Releem aide à définir les priorités.** Il s’appuie sur la charge de travail observée et les points relevés pour vous aider à déterminer les points à traiter en priorité.
4. **Releem recommande des modifications des requêtes, des index et de la configuration.** Il présente les améliorations proposées avec des explications que votre équipe peut examiner.
5. **Releem aide à appliquer les modifications de configuration et de schéma.** Appliquez les configurations recommandées et les modifications de schéma approuvées grâce à une automatisation contrôlée.
6. **Releem vous aide à mesurer l’effet des changements.** Comparez les performances des requêtes, l’utilisation des ressources et les paramètres effectifs après une modification.

<a name="getting-started"></a>

## 🚀 Premiers pas avec Releem

1. **Créez votre compte.** [Inscrivez-vous à Releem](https://releem.com/?utm_source=github&utm_medium=link&utm_campaign=signup#).
2. **Connectez votre base de données.** Installez Releem Agent en suivant le [guide d’installation](https://docs.releem.com/installation) correspondant à votre environnement.
3. **Vérifiez la connexion.** Assurez-vous que l’état de l’Agent est Connected et que des métriques récentes arrivent dans le tableau de bord.
4. **Examinez et appliquez les recommandations.** Examinez les recommandations concernant les requêtes, les index et la configuration, puis appliquez les configurations recommandées et les modifications de schéma approuvées lorsque vous êtes prêt.

Consultez les [offres actuelles](https://releem.com/pricing) du service hébergé.

<a name="security"></a>

## 🔒 Sécurité

Selon la base de données et les fonctionnalités activées, les informations collectées peuvent inclure les statistiques des ressources système, les paramètres et l’état de la base de données, les noms des schémas et des tables, leur structure et leurs index, les statistiques d’exécution des requêtes, des exemples de requêtes et des plans EXPLAIN.

Avant la connexion, examinez les privilèges requis pour votre base de données et les fonctionnalités activées. Consultez la [FAQ sur la sécurité des données](https://releem.com/faq) pour en savoir plus sur la collecte et le transfert des données par l’Agent, ainsi que la documentation de compatibilité ci-dessous pour les exigences propres à chaque moteur.

## 🗄️ Compatibilité

### Versions des bases de données

| Base de données | Versions prises en charge selon la documentation |
| --- | --- |
| MySQL | 5.5, 5.6, 5.7, 8.0, 8.4, 9 |
| MariaDB | 10, 11, 12, 13 |
| PostgreSQL | 12, 13, 14, 15, 16, 17, 18 |
| Percona Server for MySQL | 5.7, 8.0, 8.4, 9.7 |

Consultez les [bases de données, versions et autorisations prises en charge](https://docs.releem.com/supported-databases) pour connaître les exigences actuelles.

### Environnements pris en charge

- **Linux:** CentOS, CloudLinux, Debian, Ubuntu et Rocky Linux.
- **Windows Server:** 2012, 2016, 2019, 2022 et 2025.
- **Conteneurs et orchestration:** Docker et Kubernetes.
- **Amazon RDS:** MySQL, MariaDB et PostgreSQL.
- **Amazon Aurora:** MySQL et PostgreSQL.
- **Google Cloud SQL:** MySQL et PostgreSQL.
- **Azure Database for MySQL.**
- **Hébergement et clusters:** Clusters MySQL/MariaDB et WHM/cPanel.

Choisissez la procédure d’installation pour [MySQL](https://docs.releem.com/installation/mysql), [MariaDB](https://docs.releem.com/installation/mariadb) ou [PostgreSQL](https://docs.releem.com/installation/postgresql). Les environnements, les fonctionnalités et les actions automatisées disponibles dépendent de la base de données, de sa version, du déploiement, des autorisations et de l’offre.

## 🧪 Tests de performance

Ces articles historiques décrivent des environnements de test précis et des comparaisons de configurations. Leurs résultats s’appliquent aux conditions de test documentées.

- [Test comparatif de configuration MySQL 5.7](https://releem.com/blog/how-to-improve-performance-mysql57-default-configuration)
- [Test comparatif de configuration MySQL 8](https://releem.com/blog/mysql-8-performance-benchmark)
- [Comment la configuration MySQL affecte les applications web](https://releem.com/blog/web-applications-performance)

## 🤝 Assistance, retours et feuille de route

Ce dépôt est l’espace public consacré aux retours, aux problèmes et aux discussions sur la feuille de route de l’ensemble de Releem, y compris le tableau de bord, les recommandations, les intégrations et l’Agent.

- **🤝 Obtenir de l’aide :** Rejoignez la [communauté Releem sur Slack](https://join.slack.com/t/releem-community/shared_invite/zt-1j3d0vosh-AJHbDiQrzVDvLat5eqQorQ) pour poser vos questions, participer aux discussions et obtenir de l’aide pour la configuration.
- **🐛 Signaler un bug :** Consultez les [Issues](https://github.com/Releem/releem-agent/issues) avant de signaler un problème. Indiquez le composant concerné, la version de la base de données, le déploiement et les étapes de reproduction.
- **💡 Proposer des améliorations et discuter des priorités :** Utilisez les [Discussions](https://github.com/Releem/releem-agent/discussions) pour expliquer votre cas d’usage et le résultat souhaité.

Retirez des signalements publics les informations d’authentification, les informations personnelles et les détails sensibles des requêtes ou des schémas.

Suivez les actualités du produit sur [X](https://twitter.com/releemhq) et [LinkedIn](https://www.linkedin.com/company/releem).

## 🧩 Contribuer

Les retours sur le produit et les signalements de bugs reproductibles sont les bienvenus pour l’ensemble de Releem. Les contributions de code dans ce dépôt concernent l’Agent. Décrivez le problème, la modification proposée et la manière dont vous l’avez vérifiée. Consultez la [licence](../../LICENSE) du dépôt pour connaître les conditions applicables au code publié.
