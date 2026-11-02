package kernel

import "gorm.io/gorm"

// Connection is only supplied to a module's repository constructor.
type Connection struct {
	DB *gorm.DB
}
