package postgresql

import (
	"strings"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/Releem/mysqlconfigurer/utils"
	logging "github.com/google/logger"
)

type DBInfoGatherer struct {
	logger        logging.Logger
	configuration *config.Config
}

func NewDBInfoGatherer(logger logging.Logger, configuration *config.Config) *DBInfoGatherer {
	return &DBInfoGatherer{
		logger:        logger,
		configuration: configuration,
	}
}

func (DBInfo *DBInfoGatherer) GetMetrics(metrics *models.Metrics) error {
	defer utils.HandlePanic(DBInfo.configuration, DBInfo.logger)

	if metrics.DB.Info == nil {
		metrics.DB.Info = make(models.MetricGroupValue)
	}

	pgHBA := DBInfo.collectPgHBA()
	if pgHBA != nil {
		metrics.DB.Info["pg_hba"] = pgHBA
	}

	metrics.DB.Info["Extensions"] = DBInfo.collectExtensions()
	metrics.DB.Info["Users"] = DBInfo.collectUsers()
	// pg_hba entries are handed over directly: reading them back out of the
	// metrics map made the check silently depend on both the map key and the
	// order of these calls.
	metrics.DB.Info["UsersSecurityCheck"] = DBInfo.collectUsersSecurityCheck(metrics, pgHBA)
	metrics.DB.Info["PublicSchemaPermissions"] = DBInfo.collectPublicSchemaPermissions()
	metrics.DB.Info["RLS"] = DBInfo.collectRLSInfo()

	DBInfo.logger.V(5).Info("CollectMetrics PostgreSQL DBInfo ", metrics.DB.Info)
	return nil
}

func (DBInfo *DBInfoGatherer) collectExtensions() []models.MetricGroupValue {
	output := []models.MetricGroupValue{}

	rows, err := models.DB.Query(`
		SELECT
			extname,
			COALESCE(extversion, 'NULL') AS extversion,
			COALESCE(extnamespace::regnamespace::text, 'NULL') AS schema_name
		FROM pg_extension
		ORDER BY extname`)
	if err != nil {
		DBInfo.logger.Error(err)
		return output
	}
	defer rows.Close()

	for rows.Next() {
		var name, version, schemaName string
		if err := rows.Scan(&name, &version, &schemaName); err != nil {
			DBInfo.logger.Error(err)
			continue
		}
		output = append(output, models.MetricGroupValue{
			"name":       name,
			"version":    version,
			"schema":     schemaName,
			"extversion": version,
		})
	}

	return output
}

func (DBInfo *DBInfoGatherer) collectUsers() []models.MetricGroupValue {
	output := []models.MetricGroupValue{}

	rows, err := models.DB.Query(`
		SELECT
			rolname,
			rolsuper,
			rolcreaterole,
			rolcreatedb,
			rolcanlogin,
			rolreplication,
			rolbypassrls,
			rolconnlimit,
			COALESCE(
				array_to_string(
					ARRAY(
						SELECT granted_role.rolname
						FROM pg_auth_members member
						JOIN pg_roles granted_role ON granted_role.oid = member.roleid
						WHERE member.member = role_entry.oid
						ORDER BY granted_role.rolname
					),
					','
				),
				''
			) AS member_of
		FROM pg_roles role_entry
		ORDER BY rolname`)
	if err != nil {
		DBInfo.logger.Error(err)
		return output
	}
	defer rows.Close()

	for rows.Next() {
		var user string
		var roleSuper, roleCreateRole, roleCreateDB, roleCanLogin, roleReplication, roleBypassRLS bool
		var roleConnLimit int
		var memberOf string

		if err := rows.Scan(
			&user,
			&roleSuper,
			&roleCreateRole,
			&roleCreateDB,
			&roleCanLogin,
			&roleReplication,
			&roleBypassRLS,
			&roleConnLimit,
			&memberOf,
		); err != nil {
			DBInfo.logger.Error(err)
			continue
		}

		output = append(output, models.MetricGroupValue{
			"User":           user,
			"rolsuper":       roleSuper,
			"rolcreaterole":  roleCreateRole,
			"rolcreatedb":    roleCreateDB,
			"rolcanlogin":    roleCanLogin,
			"rolreplication": roleReplication,
			"rolbypassrls":   roleBypassRLS,
			"rolconnlimit":   roleConnLimit,
			"MemberOf":       memberOf,
		})
	}

	return output
}

// collectUsersSecurityCheck flags roles that are reachable remotely with
// superuser rights. pgHBAEntries may be nil when the rules could not be read,
// in which case no role can be proven remotely reachable and the check reports
// no finding rather than guessing.
func (DBInfo *DBInfoGatherer) collectUsersSecurityCheck(metrics *models.Metrics, pgHBAEntries []models.MetricGroupValue) []models.MetricGroupValue {
	usersCheck := []models.MetricGroupValue{}

	users, ok := metrics.DB.Info["Users"].([]models.MetricGroupValue)
	if !ok || len(users) == 0 {
		return usersCheck
	}

	for _, user := range users {
		username, _ := user["User"].(string)
		roleSuper, _ := user["rolsuper"].(bool)
		roleCanLogin, _ := user["rolcanlogin"].(bool)

		remoteConnSuperuser := 0
		if roleSuper && roleCanLogin && hasRemoteAccess(pgHBAEntries, username) {
			remoteConnSuperuser = 1
		}

		usersCheck = append(usersCheck, models.MetricGroupValue{
			"Remote_Conn_Superuser": remoteConnSuperuser,
			"Anonymous_User":        0,
			"Blank_Password":        0,
			"Password_As_User":      0,
		})
	}

	return usersCheck
}

func (DBInfo *DBInfoGatherer) collectPublicSchemaPermissions() models.MetricGroupValue {
	var privileges string

	err := models.DB.QueryRow(`
		SELECT CONCAT_WS(',',
			CASE WHEN has_schema_privilege('public', 'public', 'USAGE') THEN 'USAGE' END,
			CASE WHEN has_schema_privilege('public', 'public', 'CREATE') THEN 'CREATE' END
		)`).Scan(&privileges)
	if err != nil {
		DBInfo.logger.Error(err)
		return nil
	}

	return models.MetricGroupValue{"public": privileges}
}

func (DBInfo *DBInfoGatherer) collectRLSInfo() bool {
	var enabled bool

	err := models.DB.QueryRow(`
		SELECT EXISTS (
			SELECT 1
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relkind IN ('r', 'p')
				AND n.nspname NOT IN ('pg_catalog', 'information_schema')
				AND c.relrowsecurity
		)`).Scan(&enabled)
	if err != nil {
		DBInfo.logger.Error(err)
		return false
	}

	return enabled
}

// pgHBARulesUnavailable reports whether pg_hba_file_rules could not be read for
// an expected reason rather than a fault, so the miss is logged as a warning and
// the rest of the report is still delivered. Managed providers such as
// Aurora/RDS own the view with an internal role (rdsadmin) that cannot delegate
// access to a customer monitoring role, and PostgreSQL below 10 has no such view
// at all.
func pgHBARulesUnavailable(instanceType string, err error) bool {
	const (
		insufficientPrivilege = "42501"
		undefinedTable        = "42P01"
	)
	return hasPgErrorCode(err, undefinedTable) ||
		(isPGManagedInstanceType(instanceType) && hasPgErrorCode(err, insufficientPrivilege))
}

func (DBInfo *DBInfoGatherer) handlePgHBARulesUnavailable(err error) bool {
	instanceType := ""
	if DBInfo.configuration != nil {
		instanceType = DBInfo.configuration.InstanceType
	}
	if !pgHBARulesUnavailable(instanceType, err) {
		return false
	}
	DBInfo.logger.Warningf("pg_hba_file_rules is unavailable for PostgreSQL instance type %q; dependent security checks are unknown", instanceType)
	return true
}

func (DBInfo *DBInfoGatherer) collectPgHBA() []models.MetricGroupValue {
	output := []models.MetricGroupValue{}

	rows, err := models.DB.Query(`
		SELECT
			COALESCE(type, '') AS type,
			COALESCE(array_to_string(database, ','), '') AS database,
			COALESCE(array_to_string(user_name, ','), '') AS user_name,
			COALESCE(address, '') AS address,
			COALESCE(auth_method, '') AS auth_method,
			COALESCE(error, '') AS error
		FROM pg_hba_file_rules
		ORDER BY line_number`)
	if err != nil {
		if DBInfo.handlePgHBARulesUnavailable(err) {
			return nil
		}
		DBInfo.logger.Error(err)
		return nil
	}
	defer rows.Close()

	for rows.Next() {
		var entryType, database, userName, address, authMethod, entryError string
		if err := rows.Scan(&entryType, &database, &userName, &address, &authMethod, &entryError); err != nil {
			DBInfo.logger.Error(err)
			continue
		}

		output = append(output, models.MetricGroupValue{
			"type":        entryType,
			"database":    database,
			"user_name":   userName,
			"address":     address,
			"auth_method": authMethod,
			"error":       entryError,
		})
	}

	return output
}

func hasRemoteAccess(pgHBAEntries []models.MetricGroupValue, username string) bool {
	for _, entry := range pgHBAEntries {
		entryType := strings.ToLower(strings.TrimSpace(interfaceToString(entry["type"])))
		if entryType != "host" && entryType != "hostssl" && entryType != "hostnossl" {
			continue
		}

		address := strings.ToLower(strings.TrimSpace(interfaceToString(entry["address"])))
		if address == "" || address == "127.0.0.1/32" || address == "::1/128" || address == "samehost" {
			continue
		}

		userName := strings.ToLower(strings.TrimSpace(interfaceToString(entry["user_name"])))
		if userName == "" || userName == "all" {
			return true
		}

		for _, candidate := range strings.Split(userName, ",") {
			if strings.TrimSpace(candidate) == strings.ToLower(username) {
				return true
			}
		}
	}

	return false
}

func interfaceToString(value interface{}) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}
