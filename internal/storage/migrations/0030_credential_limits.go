package migrations

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

const ID0030 = "0030_credential_limits"

type credentialLimits0030 struct {
	RPMLimit         int64 `gorm:"column:rpm_limit;not null;default:0"`
	ConcurrencyLimit int64 `gorm:"column:concurrency_limit;not null;default:0"`
}

func (credentialLimits0030) TableName() string { return "credentials" }

type groupCredentialLimits0030 struct {
	CredentialRPMLimit         int64 `gorm:"column:credential_rpm_limit;not null;default:0"`
	CredentialConcurrencyLimit int64 `gorm:"column:credential_concurrency_limit;not null;default:0"`
}

func (groupCredentialLimits0030) TableName() string { return "groups" }

// Up0030 为凭据和分组增加本地限额。默认 0：凭据的 0 表示继承分组默认值，
// 分组的 0 表示不限，因此存量数据的行为保持不变。
func Up0030(db *gorm.DB) error {
	if err := ValidateRecoverable0030(db); err != nil {
		return err
	}
	for _, column := range []struct {
		model any
		name  string
		field string
	}{
		{&credentialLimits0030{}, "rpm_limit", "RPMLimit"},
		{&credentialLimits0030{}, "concurrency_limit", "ConcurrencyLimit"},
		{&groupCredentialLimits0030{}, "credential_rpm_limit", "CredentialRPMLimit"},
		{&groupCredentialLimits0030{}, "credential_concurrency_limit", "CredentialConcurrencyLimit"},
	} {
		if db.Migrator().HasColumn(column.model, column.name) {
			continue
		}
		if err := db.Migrator().AddColumn(column.model, column.field); err != nil {
			return fmt.Errorf("add %s: %w", column.name, err)
		}
	}
	return Validate0030(db)
}

func ValidateRecoverable0030(db *gorm.DB) error {
	switch db.Dialector.Name() {
	case "sqlite", "mysql", "postgres":
	default:
		return fmt.Errorf("unsupported credential limits migration driver %q", db.Dialector.Name())
	}
	if !db.Migrator().HasTable(&credentialLimits0030{}) {
		return fmt.Errorf("credentials table is missing")
	}
	if !db.Migrator().HasTable(&groupCredentialLimits0030{}) {
		return fmt.Errorf("groups table is missing")
	}
	return nil
}

func Validate0030(db *gorm.DB) error {
	checks := []struct {
		model  any
		column string
	}{
		{&credentialLimits0030{}, "rpm_limit"},
		{&credentialLimits0030{}, "concurrency_limit"},
		{&groupCredentialLimits0030{}, "credential_rpm_limit"},
		{&groupCredentialLimits0030{}, "credential_concurrency_limit"},
	}
	for _, check := range checks {
		if err := validateLimitColumn(db, check.model, check.column); err != nil {
			return err
		}
	}
	return nil
}

func validateLimitColumn(db *gorm.DB, model any, name string) error {
	columns, err := db.Migrator().ColumnTypes(model)
	if err != nil {
		return err
	}
	for _, column := range columns {
		if column.Name() != name {
			continue
		}
		switch strings.ToLower(column.DatabaseTypeName()) {
		case "int", "integer", "int4", "int8", "bigint":
		default:
			return fmt.Errorf("column %s must be an integer", name)
		}
		if nullable, known := column.Nullable(); !known || nullable {
			return fmt.Errorf("column %s must not be nullable", name)
		}
		value, known := column.DefaultValue()
		if !known || strings.Trim(strings.SplitN(value, "::", 2)[0], "'\"() ") != "0" {
			return fmt.Errorf("column %s default must be zero", name)
		}
		return nil
	}
	return fmt.Errorf("column %s is missing", name)
}
