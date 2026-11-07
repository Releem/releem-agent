<h1 align="center"><a href="https://releem.com/"><img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem-icon-top.png" width="24" height="24" alt="Логотип Releem" /></a> Releem</h1>

**Инструмент настройки производительности MySQL с ИИ и контролируемой автоматизацией**

Releem является инструментом настройки производительности MySQL с ИИ и контролируемой автоматизацией и вашим советником по базам данных с первого решения по оптимизации.

Для инженеров, администраторов баз данных и инфраструктурных команд, отвечающих за производительность MySQL в рабочей среде. Повышайте производительность запросов и эффективность использования ресурсов с помощью рекомендаций на основе реальной нагрузки. Проверяйте, что требует внимания, по мере изменения запросов, трафика и данных.

<p align="center">
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/MySQL-4479A1?logo=mysql&amp;logoColor=white" alt="MySQL" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/MariaDB-003545?logo=mariadb&amp;logoColor=white" alt="MariaDB" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/PostgreSQL-336791?logo=postgresql&amp;logoColor=white" alt="PostgreSQL" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/Percona_Server_for_MySQL-orange" alt="Percona Server for MySQL" /></a>
  <br />
  <a href="https://hub.docker.com/r/releem/releem-agent"><img src="https://img.shields.io/docker/pulls/releem/releem-agent?logo=docker&amp;logoColor=white" alt="Загрузки Docker" /></a>
    <a href="https://docs.releem.com/installation"><img src="https://img.shields.io/badge/platforms-Linux%20%7C%20Windows%20%7C%20Docker%20%7C%20K8s%20%7C%20AWS%20%7C%20GCP%20%7C%20Azure%20%7C%20WHM%2FcPanel%20%7C%20Clusters-555555" alt="Платформы: Linux, Windows, Docker, Kubernetes, Amazon RDS, Amazon Aurora, Google Cloud SQL, Azure Database for MySQL, WHM/cPanel и кластеры" /></a>
  <a href="https://github.com/Releem/releem-agent/stargazers"><img src="https://img.shields.io/github/stars/Releem/releem-agent?style=flat&amp;logo=github" alt="Звёзды GitHub" /></a>
  <a href="https://github.com/Releem/releem-agent"><img src="https://img.shields.io/badge/Agent-open_source-2ea44f?logo=github" alt="Agent с открытым исходным кодом" /></a>
  <a href="https://join.slack.com/t/releem-community/shared_invite/zt-1j3d0vosh-AJHbDiQrzVDvLat5eqQorQ"><img src="https://img.shields.io/badge/Slack-Join_community-4A154B?logo=slack&amp;logoColor=white" alt="Сообщество в Slack" /></a>
</p>

<p align="center">
  <a href="../../README.md">English</a> •
  <b>Русский</b> •
  <a href="README.es.md">Español</a> •
  <a href="README.pt.md">Português</a> •
  <a href="README.zh.md">简体中文</a> •
  <a href="README.fr.md">Français</a> •
  <a href="README.id.md">Bahasa Indonesia</a>
</p>

<p align="center">
  <a href="#getting-started">Начать работу</a> |
  <a href="https://docs.releem.com/">Документация</a> |
  <a href="https://releem.com/mysql-configuration-tuning">Настройка конфигурации MySQL</a> |
  <a href="https://releem.com/sql-query-optimization">Оптимизация SQL-запросов</a> |
  <a href="https://releem.com/compare/mysqltuner">Сравнение с MySQLTuner</a> |
  <a href="#security">Безопасность</a>
</p>

В этом репозитории находится **Releem Agent** с открытым исходным кодом, который подключает вашу базу данных к облачному сервису Releem по подписке. Здесь также собираются отзывы, Issues и обсуждения планов развития всего продукта Releem.

<p align="center">
  <img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem_dashboard.png" width="90%" alt="Панель управления Releem" />
</p>

## ⚡ Почему Releem?

- **[Оптимизация запросов и индексов](https://releem.com/sql-query-optimization):** Изучайте рекомендации по запросам и [индексам на основе реальной нагрузки](https://docs.releem.com/recommendations) для повышения производительности запросов MySQL.
- **[Настройка конфигурации MySQL](https://releem.com/mysql-configuration-tuning):** Изучайте настройки на основе реальной нагрузки. Тюнинг MySQL с применением машинного обучения помогает эффективнее использовать ресурсы.
- **Контролируемая автоматизация:** Применяйте поддерживаемые изменения конфигурации и допустимые одобренные изменения схемы по описанным процедурам. Ваша команда контролирует, что и когда изменяется.
- **[Мониторинг и проверки состояния](https://releem.com/health-checks):** Используйте метрики базы данных и проверки состояния, чтобы определять, что требует внимания, и сравнивать наблюдаемую производительность и использование ресурсов после изменений.

См. [процесс настройки конфигурации](https://docs.releem.com/recommendations/configuration-tuning/mysql-tuning-process) и [процедуру применения изменений схемы](https://docs.releem.com/recommendations/query-optimization/automatic-schema-changes) для ознакомления с поддерживаемыми изменениями и порядком их применения.

<p align="center">
  <strong>Найдите и добавьте недостающие индексы MySQL с Releem</strong><br /><br />
  <a href="https://www.youtube.com/watch?v=Pd-DUK0so9Y">
    <img src="https://img.youtube.com/vi/Pd-DUK0so9Y/hqdefault.jpg" width="480" alt="Посмотрите, как найти и добавить недостающие индексы MySQL с Releem" />
  </a>
  <br />
  <a href="https://www.youtube.com/watch?v=Pd-DUK0so9Y">▶ Смотреть: Найдите и добавьте недостающие индексы MySQL с Releem</a>
</p>

<p align="center">
  <strong>Примените рекомендованную конфигурацию MySQL</strong><br /><br />
  <a href="https://youtu.be/XqBZPGRlrGs">
    <img src="https://raw.githubusercontent.com/Releem/docs/main/assets/images/applying-conf-dark.gif"
         width="80%"
         alt="Панель Releem: изучение обоснования, проверка необходимости перезапуска и применение рекомендованной конфигурации MySQL" />
  </a>
  <br />
  <a href="https://youtu.be/XqBZPGRlrGs">▶ Смотреть 15-секундную демонстрацию на YouTube</a>
</p>

<p align="center">
  <strong>Примените конфигурацию через CLI Releem Agent</strong><br /><br />
  <img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem-applying.gif" width="80%" alt="Применение конфигурации с помощью Releem Agent" />
</p>

## 🧭 Как это работает

1. **Анализирует нагрузку MySQL.** Собирает сведения об активности запросов, настройках базы данных и ресурсах, чтобы понять, как работает ваша база данных.
2. **Находит возможности для оптимизации производительности.** Выявляет медленные запросы MySQL, часто выполняемые запросы и особенности конфигурации, которые могут влиять на производительность.
3. **Помогает определить приоритеты.** Использует наблюдения за нагрузкой и выявленные проблемы, чтобы помочь выбрать, чем заняться в первую очередь.
4. **Рекомендует изменения запросов, индексов и конфигурации.** Предлагает улучшения с пояснениями для проверки вашей командой.
5. **Помогает применять поддерживаемые изменения.** Применяйте поддерживаемые изменения конфигурации и одобренные изменения схемы с помощью контролируемой автоматизации.
6. **Помогает оценить результат.** Сравнивайте производительность запросов, использование ресурсов и фактически действующие настройки после изменения.

<a name="getting-started"></a>

## 🚀 Начало работы с Releem

1. **Создайте аккаунт.** [Зарегистрируйтесь в Releem](https://releem.com/?utm_source=github&utm_medium=link&utm_campaign=signup#).
2. **Подключите базу данных.** Установите Releem Agent по [руководству по установке](https://docs.releem.com/installation) для вашей среды.
3. **Проверьте подключение.** Убедитесь, что у Agent отображается статус Connected и в панели появляются актуальные метрики.
4. **Изучите и примените рекомендации.** Проверьте рекомендации по запросам, индексам и конфигурации, затем примените поддерживаемые изменения, когда будете готовы.

См. [актуальные тарифы](https://releem.com/pricing) облачного сервиса.

<a name="security"></a>

## 🔒 Безопасность

В зависимости от базы данных и включённых функций собираемая информация может включать статистику системных ресурсов, настройки и состояние базы данных, имена схем и таблиц, их структуру и индексы, статистику выполнения запросов, примеры запросов и планы EXPLAIN.

Перед подключением ознакомьтесь с необходимыми правами доступа для вашей базы данных и включённых функций. См. [FAQ по безопасности данных](https://releem.com/faq) для получения информации о сборе и передаче данных Agent, а также документацию по совместимости ниже для требований конкретного движка.

## 🗄️ Совместимость

### Версии баз данных

| База данных | Поддерживаемые версии согласно документации |
| --- | --- |
| MySQL | 5.5, 5.6, 5.7, 8.0, 8.4, 9 |
| MariaDB | 10, 11, 12, 13 |
| PostgreSQL | 12, 13, 14, 15, 16, 17, 18 |
| Percona Server for MySQL | 5.7, 8.0, 8.4, 9.7 |

Проверьте [поддерживаемые базы данных, версии и права доступа](https://docs.releem.com/supported-databases) для уточнения актуальных требований.

### Поддерживаемые среды

- **Linux:** CentOS, CloudLinux, Debian, Ubuntu и Rocky Linux.
- **Windows Server:** 2012, 2016, 2019, 2022 и 2025.
- **Контейнеры и оркестрация:** Docker и Kubernetes.
- **Amazon RDS:** MySQL, MariaDB и PostgreSQL.
- **Amazon Aurora:** MySQL и PostgreSQL.
- **Google Cloud SQL:** MySQL и PostgreSQL.
- **Azure Database for MySQL.**
- **Хостинг и кластеры:** Кластеры MySQL/MariaDB и WHM/cPanel.

Выберите вариант установки для [MySQL](https://docs.releem.com/installation/mysql), [MariaDB](https://docs.releem.com/installation/mariadb) или [PostgreSQL](https://docs.releem.com/installation/postgresql). Доступные среды, функции и автоматизированные действия зависят от базы данных, версии, способа развёртывания, прав доступа и тарифа.

## 🧪 Тесты производительности

В этих исторических статьях описаны конкретные тестовые среды и сравнения конфигураций. Результаты применимы к указанным условиям тестирования.

- [Тестирование конфигурации MySQL 5.7](https://releem.com/blog/how-to-improve-performance-mysql57-default-configuration)
- [Тестирование конфигурации MySQL 8](https://releem.com/blog/mysql-8-performance-benchmark)
- [Как конфигурация MySQL влияет на веб-приложения](https://releem.com/blog/web-applications-performance)

## 🤝 Поддержка, отзывы и планы развития

Этот репозиторий служит общедоступным местом для отзывов, сообщений о проблемах и обсуждения планов развития всего Releem, включая панель управления, рекомендации, интеграции и Agent.

- **🤝 Получить помощь:** Присоединяйтесь к [сообществу Releem в Slack](https://join.slack.com/t/releem-community/shared_invite/zt-1j3d0vosh-AJHbDiQrzVDvLat5eqQorQ) для вопросов, обсуждений и помощи с настройкой.
- **🐛 Сообщить об ошибке:** Проверьте [Issues](https://github.com/Releem/releem-agent/issues) перед созданием сообщения о проблеме. Укажите затронутый компонент, версию базы данных, способ развёртывания и шаги для воспроизведения.
- **💡 Предложить улучшение и обсудить приоритеты развития:** Используйте [Discussions](https://github.com/Releem/releem-agent/discussions) для описания вашего сценария использования и желаемого результата.

Не включайте в публичные сообщения учётные данные, персональную информацию и конфиденциальные сведения о запросах или схемах.

Следите за обновлениями продукта в [X](https://twitter.com/releemhq) и [LinkedIn](https://www.linkedin.com/company/releem).

## 🧩 Участие в разработке

Мы приветствуем отзывы и воспроизводимые сообщения об ошибках по всему продукту Releem. Изменения кода в этом репозитории относятся к Agent. Опишите проблему, предлагаемое изменение и способ его проверки. Условия использования опубликованного кода см. в [лицензии](../../LICENSE) репозитория.
