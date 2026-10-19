package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteReport(t *testing.T) {
	t.Parallel()

	report := []byte("{\n  \"root_url\": \"https://example.test\"\n}")
	var output bytes.Buffer

	require.NoError(t, writeReport(&output, report))
	require.Equal(t, string(report)+"\n", output.String())
}
