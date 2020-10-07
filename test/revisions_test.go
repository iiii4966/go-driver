package test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/arangodb/go-driver"
	"github.com/stretchr/testify/require"
)

func TestRevisionTree(t *testing.T) {
	if getTestMode() != testModeSingle {
		t.Skipf("Not a single")
	}
	c := createClientFromEnv(t, true)
	skipBelowVersion(c, "3.7", t)

	db := ensureDatabase(nil, c, "revision_tree", nil, t)
	col := ensureCollection(nil, db, "revision_tree", nil, t)

	var noOfDocuments int = 80000
	expectedDocuments := make([]interface{}, 0, noOfDocuments)
	for i := 0; i < noOfDocuments; i++ {
		expectedDocuments = append(expectedDocuments, UserDoc{
			Name: fmt.Sprintf("User%d", 1),
			Age:  i,
		})
	}

	_, _, err := col.CreateDocuments(context.Background(), expectedDocuments)
	require.NoErrorf(t, err, "Failed to create new documents: %s", describe(err))

	batch, err := c.Replication().CreateBatch(context.Background(), db, 123, time.Hour)
	require.NoError(t, err)
	defer batch.Delete(context.Background())

	getTree := func() (driver.RevisionTree, error) {
		timeoutCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		return c.Replication().GetRevisionTree(timeoutCtx, db, batch.BatchID(), col.Name())
	}

	tree, err := getTree()

	if err != nil {
		if driver.IsArangoErrorWithCode(err, http.StatusNotImplemented) {
			t.Skip("Collection '" + col.Name() + "' does not support revision-based replication")
		}

		require.NoError(t, err)
	}

	require.NotEmpty(t, tree.Version)
	require.NotEmpty(t, tree.RangeMin)
	require.NotEmpty(t, tree.RangeMax)
	require.NotEmpty(t, tree.Nodes)

	branchFactor := 8.0
	noOfLeaves := 0
	for i := 0; i <= tree.MaxDepth; i++ {
		noOfLeaves += int(math.Pow(branchFactor, float64(i)))
	}
	require.Equalf(t, noOfLeaves, len(tree.Nodes), "Number of leaves in the revision tree is not correct")

	getRanges := func() driver.Revisions {
		timeoutCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		rangeRevisions := []driver.RevisionMinMax{{tree.RangeMin, tree.RangeMax}}
		var resume driver.RevisionUInt64
		revisions := make(driver.Revisions, 0)

		for {
			ranges, err := c.Replication().GetRevisionsByRanges(timeoutCtx, db, batch.BatchID(), col.Name(),
				rangeRevisions, resume)
			require.NoError(t, err)

			if len(ranges.Ranges[0]) == 0 {
				// let's try again because we should get ranges at the end. There is a one minute timeout for it
				continue
			}

			revisions = append(revisions, ranges.Ranges[0]...)

			if ranges.Resume == 0 {
				break
			}
			resume = ranges.Resume
		}
		return revisions
	}

	revisions := getRanges()
	require.NotEmpty(t, revisions)
	require.Lenf(t, revisions, noOfDocuments, "Number of revisions ranges is not correct")

	getDocuments := func() ([]map[string]interface{}, error) {
		timeoutCtx, cancel := context.WithTimeout(context.Background(), time.Second*30)
		defer cancel()

		return c.Replication().GetRevisionDocuments(timeoutCtx, db, batch.BatchID(), col.Name(), revisions)
	}

	documents, err := getDocuments()
	require.NoError(t, err)
	require.Lenf(t, documents, noOfDocuments, "Number of documents is not equal")

	for i, d := range documents {
		user := UserDoc{}
		bytes, _ := json.Marshal(d)
		json.Unmarshal(bytes, &user)
		require.Equalf(t, user, expectedDocuments[i], "Documents should be the same")
	}
}
