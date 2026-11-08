package candidate

import (
	"slices"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/model"
)

// The fixtures key_lookup1, key_lookup2 and rid_lookup2 were captured by
// SET SHOWPLAN_XML ON on SQL Server 2022 in a throwaway database
// godta_fixa_k1, 200 000 rows in each table, statistics in full scan:
//
//	CREATE TABLE dbo.Orders (OrderID int IDENTITY CONSTRAINT PK_Orders PRIMARY KEY CLUSTERED,
//	  CustomerID int, Status tinyint, OrderDate datetime2(0), Amount decimal(10,2), Pad char(200));
//	CREATE INDEX IX_Orders_Customer ON dbo.Orders (CustomerID);   -- 5000 customers, 5 statuses
//	CREATE TABLE dbo.H (x int, y int, z int, pad char(200));      -- a heap
//	CREATE INDEX IX_H_x ON dbo.H (x);
//
// key_lookup2: SELECT OrderID, OrderDate, Amount FROM dbo.Orders WHERE CustomerID = 42 AND Status = 1
// key_lookup1: SELECT OrderDate, Amount FROM dbo.Orders WHERE CustomerID = 42
// rid_lookup2: SELECT z FROM dbo.H WHERE x = 42 AND y = 1
//
// Each plan is an Index Seek on the nonclustered index plus a lookup (Key
// Lookup, or RID Lookup on the heap) whose seek is on the row locator and
// whose residual predicate holds the second column.
const (
	tblOrders = 901578250
	tblHeapH  = 949578421
	lookupDB  = "godta_fixa_k1"
)

const (
	colOrderID = iota + 1
	colCustomerID
	colStatus
	colOrderDate
	colAmount
	colOrderPad
)

func lookupWorkload() *model.Workload {
	cols := func(names ...string) []model.Column {
		var out []model.Column
		for i, n := range names {
			out = append(out, model.Column{ID: i + 1, Name: n, Type: "int"})
		}
		return out
	}
	return &model.Workload{
		Database: model.DBInfo{Name: lookupDB, Collation: "SQL_Latin1_General_CP1_CI_AS"},
		Tables: map[int]*model.Table{
			tblOrders: {ObjectID: tblOrders, Schema: "dbo", Name: "Orders",
				Columns: cols("OrderID", "CustomerID", "Status", "OrderDate", "Amount", "Pad"),
				Indexes: []model.Index{
					{ObjectID: tblOrders, IndexID: 1, Name: "PK_Orders", Kind: model.KindClustered,
						Keys: []model.KeyCol{{ColumnID: colOrderID}}, Unique: true, PrimaryKey: true},
					{ObjectID: tblOrders, IndexID: 2, Name: "IX_Orders_Customer", Kind: model.KindNonclustered,
						Keys: []model.KeyCol{{ColumnID: colCustomerID}}},
				}},
			tblHeapH: {ObjectID: tblHeapH, Schema: "dbo", Name: "H", IsHeap: true, Columns: cols("x", "y", "z", "pad"),
				Indexes: []model.Index{
					{ObjectID: tblHeapH, IndexID: 0, Kind: model.KindHeap},
					{ObjectID: tblHeapH, IndexID: 2, Name: "IX_H_x", Kind: model.KindNonclustered, Keys: []model.KeyCol{{ColumnID: 1}}},
				}},
		},
	}
}

// TestKeyLookupSeekNotKey: the seek of a Key Lookup is on the clustered key
// (OrderID = [OrderID]), the row locator, not a predicate of the query. The
// candidate is (CustomerID, Status), Status coming from the lookup's
// residual predicate, with the lookup's output as includes; nothing leads
// with OrderID.
func TestKeyLookupSeekNotKey(t *testing.T) {
	w := lookupWorkload()
	w.Queries = []model.Query{{QueryID: 6, Plan: loadPlan(t, "key_lookup2.sqlplan")}}
	cands := Generate(w)
	for _, c := range cands {
		if len(c.Keys) > 0 && c.Keys[0].ColumnID == colOrderID {
			t.Errorf("candidate led by the clustered key OrderID: keys %v include %v", c.Keys, c.Include)
		}
	}
	c := one(t, cands, tblOrders, keys(asc(colCustomerID), asc(colStatus)))
	if !slices.Equal(c.Include, []int{colOrderDate, colAmount}) {
		t.Errorf("include %v, want (OrderDate, Amount), the output of the Key Lookup", c.Include)
	}
	if c.EqCount != 2 {
		t.Errorf("EqCount %d, want 2", c.EqCount)
	}
}

// TestRIDLookupSeekNotKey: the same on a heap, where the RID Lookup seeks on
// the bookmark.
func TestRIDLookupSeekNotKey(t *testing.T) {
	w := lookupWorkload()
	w.Queries = []model.Query{{QueryID: 1, Plan: loadPlan(t, "rid_lookup2.sqlplan")}}
	cands := Generate(w)
	if len(cands) != 1 {
		t.Fatalf("%d candidates, want 1: %+v", len(cands), cands)
	}
	c := one(t, cands, tblHeapH, keys(asc(1), asc(2)))
	if !slices.Equal(c.Include, []int{3}) {
		t.Errorf("include %v, want (z), the output of the RID Lookup", c.Include)
	}
}

// TestKeyLookupWidensExisting: a lookup over an existing nonclustered index
// whose keys are the query's equality gives a widen of that index (spec
// objective 7), keys (CustomerID), includes (OrderDate, Amount).
func TestKeyLookupWidensExisting(t *testing.T) {
	w := lookupWorkload()
	w.Queries = []model.Query{{QueryID: 7, Plan: loadPlan(t, "key_lookup1.sqlplan")}}
	gen := Generate(w)
	if len(gen) != 1 {
		t.Fatalf("%d candidates, want 1: %+v", len(gen), gen)
	}
	if !slices.Equal(gen[0].Keys, keys(asc(colCustomerID))) {
		t.Errorf("generated keys %v, want (CustomerID)", gen[0].Keys)
	}
	c := ConfrontAll(gen, w)[0]
	if c.Decision != model.DecisionWiden || c.Existing != "IX_Orders_Customer" {
		t.Errorf("decision %q existing %q, want widen IX_Orders_Customer", c.Decision, c.Existing)
	}
	if !slices.Equal(c.Keys, keys(asc(colCustomerID))) || !slices.Equal(c.Include, []int{colOrderDate, colAmount}) {
		t.Errorf("keys %v include %v, want (CustomerID) INCLUDE (OrderDate, Amount)", c.Keys, c.Include)
	}
}
