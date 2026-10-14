package httpapi_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/testutil"
)

var testPool *pgxpool.Pool

func TestMain(m *testing.M) { testutil.RunMain(m, &testPool) }
