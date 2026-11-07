<h1 align="center"><a href="https://releem.com/"><img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem-icon-top.png" width="24" height="24" alt="Logotipo de Releem" /></a> Releem</h1>

**Herramienta de optimización del rendimiento de MySQL con IA y automatización controlada**

Releem es una herramienta de optimización del rendimiento de MySQL con IA y automatización controlada que actúa como tu asesor de bases de datos desde la primera decisión de optimización.

Para ingenieros, administradores de bases de datos y equipos de infraestructura responsables del rendimiento de MySQL en producción. Mejora el rendimiento de las consultas y el uso de recursos con recomendaciones basadas en la carga de trabajo, y revisa qué necesita atención a medida que cambian las consultas, el tráfico y los datos.

<p align="center">
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/MySQL-4479A1?logo=mysql&amp;logoColor=white" alt="MySQL" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/MariaDB-003545?logo=mariadb&amp;logoColor=white" alt="MariaDB" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/PostgreSQL-336791?logo=postgresql&amp;logoColor=white" alt="PostgreSQL" /></a>
  <a href="https://docs.releem.com/supported-databases"><img src="https://img.shields.io/badge/Percona_Server_for_MySQL-orange" alt="Percona Server for MySQL" /></a>
  <br />
  <a href="https://hub.docker.com/r/releem/releem-agent"><img src="https://img.shields.io/docker/pulls/releem/releem-agent?logo=docker&amp;logoColor=white" alt="Descargas de Docker" /></a>
    <a href="https://docs.releem.com/installation"><img src="https://img.shields.io/badge/platforms-Linux%20%7C%20Windows%20%7C%20Docker%20%7C%20K8s%20%7C%20AWS%20%7C%20GCP%20%7C%20Azure%20%7C%20WHM%2FcPanel%20%7C%20Clusters-555555" alt="Plataformas: Linux, Windows, Docker, Kubernetes, Amazon RDS, Amazon Aurora, Google Cloud SQL, Azure Database for MySQL, WHM/cPanel y clústeres" /></a>
  <a href="https://github.com/Releem/releem-agent/stargazers"><img src="https://img.shields.io/github/stars/Releem/releem-agent?style=flat&amp;logo=github" alt="Estrellas de GitHub" /></a>
  <a href="https://github.com/Releem/releem-agent"><img src="https://img.shields.io/badge/Agent-open_source-2ea44f?logo=github" alt="Agent de código abierto" /></a>
  <a href="https://join.slack.com/t/releem-community/shared_invite/zt-1j3d0vosh-AJHbDiQrzVDvLat5eqQorQ"><img src="https://img.shields.io/badge/Slack-Join_community-4A154B?logo=slack&amp;logoColor=white" alt="Comunidad en Slack" /></a>
</p>

<p align="center">
  <a href="../../README.md">English</a> •
  <a href="README.ru.md">Русский</a> •
  <b>Español</b> •
  <a href="README.pt.md">Português</a> •
  <a href="README.zh.md">简体中文</a> •
  <a href="README.fr.md">Français</a> •
  <a href="README.id.md">Bahasa Indonesia</a>
</p>

<p align="center">
  <a href="#getting-started">Primeros pasos</a> |
  <a href="https://docs.releem.com/">Documentación</a> |
  <a href="https://releem.com/mysql-configuration-tuning">Ajuste de configuración de MySQL</a> |
  <a href="https://releem.com/sql-query-optimization">Optimización de consultas SQL</a> |
  <a href="https://releem.com/compare/mysqltuner">Comparación con MySQLTuner</a> |
  <a href="#security">Seguridad</a>
</p>

Este repositorio contiene **Releem Agent**, de código abierto, que conecta tu base de datos al servicio alojado de Releem por suscripción. También reúne comentarios, Issues y debates sobre la hoja de ruta de todo el producto Releem.

<p align="center">
  <img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem_dashboard.png" width="90%" alt="Panel de Releem" />
</p>

## ⚡ ¿Por qué Releem?

- **[Optimización de consultas e índices](https://releem.com/sql-query-optimization):** Revisa las recomendaciones sobre consultas e [índices basadas en la carga de trabajo](https://docs.releem.com/recommendations) para mejorar el rendimiento de las consultas MySQL.
- **[Ajuste de configuración de MySQL](https://releem.com/mysql-configuration-tuning):** Revisa los ajustes basados en la carga de trabajo, con tuning MySQL apoyado en aprendizaje automático, para usar los recursos de forma más eficiente.
- **Automatización controlada:** Aplica los cambios de configuración admitidos y los cambios de esquema compatibles y aprobados mediante los procesos documentados. Tu equipo conserva el control sobre qué se cambia y cuándo.
- **[Monitorización y comprobaciones de estado](https://releem.com/health-checks):** Usa las métricas y las comprobaciones de estado de la base de datos para identificar qué necesita atención y comparar el rendimiento observado y el uso de recursos después de los cambios.

Consulta el [proceso de ajuste de la configuración](https://docs.releem.com/recommendations/configuration-tuning/mysql-tuning-process) y el [proceso de aplicación de cambios de esquema](https://docs.releem.com/recommendations/query-optimization/automatic-schema-changes) para conocer los cambios admitidos y los pasos para aplicarlos.

<p align="center">
  <strong>Encuentra y añade los índices que faltan en MySQL con Releem</strong><br /><br />
  <a href="https://www.youtube.com/watch?v=Pd-DUK0so9Y">
    <img src="https://img.youtube.com/vi/Pd-DUK0so9Y/hqdefault.jpg" width="480" alt="Descubre cómo encontrar y añadir los índices que faltan en MySQL con Releem" />
  </a>
  <br />
  <a href="https://www.youtube.com/watch?v=Pd-DUK0so9Y">▶ Ver: Encuentra y añade los índices que faltan en MySQL con Releem</a>
</p>

<p align="center">
  <strong>Aplica una configuración recomendada de MySQL</strong><br /><br />
  <a href="https://youtu.be/XqBZPGRlrGs">
    <img src="https://raw.githubusercontent.com/Releem/docs/main/assets/images/applying-conf-dark.gif"
         width="80%"
         alt="Panel de Releem que muestra cómo revisar la justificación, comprobar si hace falta reiniciar y aplicar una configuración recomendada de MySQL" />
  </a>
  <br />
  <a href="https://youtu.be/XqBZPGRlrGs">▶ Ver la demostración de 15 segundos en YouTube</a>
</p>

<p align="center">
  <strong>Aplica una configuración con la CLI de Releem Agent</strong><br /><br />
  <img src="https://raw.githubusercontent.com/releem/docs/master/assets/images/releem-applying.gif" width="80%" alt="Aplicación de una configuración con Releem Agent" />
</p>

## 🧭 Cómo funciona

1. **Analiza la carga de trabajo de MySQL.** Recopila la actividad de las consultas, los ajustes de la base de datos y las métricas de recursos para comprender cómo funciona tu base de datos.
2. **Identifica oportunidades de optimización del rendimiento.** Destaca consultas lentas o frecuentes y hallazgos de configuración que pueden afectar al rendimiento.
3. **Ayuda a establecer prioridades.** Usa las observaciones sobre la carga de trabajo y los hallazgos para ayudarte a decidir qué abordar primero.
4. **Recomienda cambios en consultas, índices y configuración.** Presenta propuestas de mejora con explicaciones para que tu equipo las revise.
5. **Ayuda a aplicar los cambios admitidos.** Aplica los cambios de configuración admitidos y los cambios de esquema aprobados mediante automatización controlada.
6. **Ayuda a medir el efecto.** Compara el rendimiento de las consultas, el uso de recursos y los ajustes efectivos después de un cambio.

<a name="getting-started"></a>

## 🚀 Primeros pasos con Releem

1. **Crea tu cuenta.** [Regístrate en Releem](https://releem.com/?utm_source=github&utm_medium=link&utm_campaign=signup#).
2. **Conecta tu base de datos.** Instala Releem Agent siguiendo la [guía de instalación](https://docs.releem.com/installation) correspondiente a tu entorno.
3. **Comprueba la conexión.** Confirma que el estado del Agent sea Connected y que el panel reciba métricas actualizadas.
4. **Revisa y aplica las recomendaciones.** Revisa las recomendaciones sobre consultas, índices y configuración, y aplica los cambios admitidos cuando estés preparado.

Consulta los [planes actuales](https://releem.com/pricing) del servicio alojado.

<a name="security"></a>

## 🔒 Seguridad

Según la base de datos y las funciones habilitadas, la información recopilada puede incluir estadísticas de recursos del sistema, ajustes y estado de la base de datos, nombres de esquemas y tablas, su estructura e índices, estadísticas de ejecución de consultas, ejemplos de consultas y planes EXPLAIN.

Antes de conectar la base de datos, revisa los permisos necesarios para tu motor y las funciones habilitadas. Consulta las [preguntas frecuentes sobre seguridad de los datos](https://releem.com/faq) para obtener información sobre la recopilación y transmisión de datos del Agent, y la documentación de compatibilidad que aparece más abajo para conocer los requisitos de cada motor.

## 🗄️ Compatibilidad

### Versiones de bases de datos

| Base de datos | Versiones admitidas según la documentación |
| --- | --- |
| MySQL | 5.5, 5.6, 5.7, 8.0, 8.4, 9 |
| MariaDB | 10, 11, 12, 13 |
| PostgreSQL | 12, 13, 14, 15, 16, 17, 18 |
| Percona Server for MySQL | 5.7, 8.0, 8.4, 9.7 |

Consulta las [bases de datos, versiones y permisos admitidos](https://docs.releem.com/supported-databases) para conocer los requisitos actuales.

### Entornos admitidos

- **Linux:** CentOS, CloudLinux, Debian, Ubuntu y Rocky Linux.
- **Windows Server:** 2012, 2016, 2019, 2022 y 2025.
- **Contenedores y orquestación:** Docker y Kubernetes.
- **Amazon RDS:** MySQL, MariaDB y PostgreSQL.
- **Amazon Aurora:** MySQL y PostgreSQL.
- **Google Cloud SQL:** MySQL y PostgreSQL.
- **Azure Database for MySQL.**
- **Hosting y clústeres:** Clústeres MySQL/MariaDB y WHM/cPanel.

Elige el procedimiento de instalación para [MySQL](https://docs.releem.com/installation/mysql), [MariaDB](https://docs.releem.com/installation/mariadb) o [PostgreSQL](https://docs.releem.com/installation/postgresql). Los entornos, las funciones y las acciones automatizadas disponibles dependen de la base de datos, la versión, el despliegue, los permisos y el plan.

## 🧪 Pruebas de rendimiento

Estos artículos históricos describen entornos de pruebas y comparaciones de configuración específicos. Sus resultados se aplican a las condiciones documentadas.

- [Prueba comparativa de configuración de MySQL 5.7](https://releem.com/blog/how-to-improve-performance-mysql57-default-configuration)
- [Prueba comparativa de configuración de MySQL 8](https://releem.com/blog/mysql-8-performance-benchmark)
- [Cómo afecta la configuración de MySQL a las aplicaciones web](https://releem.com/blog/web-applications-performance)

## 🤝 Soporte, comentarios y hoja de ruta

Este repositorio es el espacio público para comentarios, incidencias y debates sobre la hoja de ruta de todo Releem, incluidos el panel, las recomendaciones, las integraciones y el Agent.

- **🤝 Obtén ayuda:** Únete a la [comunidad de Releem en Slack](https://join.slack.com/t/releem-community/shared_invite/zt-1j3d0vosh-AJHbDiQrzVDvLat5eqQorQ) para hacer preguntas, participar en debates y recibir ayuda con la configuración.
- **🐛 Informa de un error:** Busca en [Issues](https://github.com/Releem/releem-agent/issues) antes de informar de un problema. Incluye el componente afectado, la versión de la base de datos, el tipo de despliegue y los pasos para reproducirlo.
- **💡 Sugiere mejoras y debate las prioridades de la hoja de ruta:** Usa [Discussions](https://github.com/Releem/releem-agent/discussions) para explicar tu caso de uso y el resultado que necesitas.

Elimina las credenciales, la información personal y los detalles sensibles de consultas o esquemas de los informes públicos.

Sigue las novedades del producto en [X](https://twitter.com/releemhq) y [LinkedIn](https://www.linkedin.com/company/releem).

## 🧩 Contribuciones

Agradecemos los comentarios y los informes de errores reproducibles sobre todo Releem. Las contribuciones de código en este repositorio corresponden al Agent. Describe el problema, el cambio propuesto y cómo lo verificaste. Consulta la [licencia](../../LICENSE) del repositorio para conocer las condiciones del código publicado.
