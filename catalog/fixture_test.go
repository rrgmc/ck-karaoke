package catalog

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Song and artist ids in the fixture, named so the tests read as intent.
const (
	songADayInTheLife = 101
	songHeyJude       = 102
	songTomSawyer     = 201
	songLimelight     = 202
	songCoracao       = 301
	songAgua          = 302
	songAFlorDaPele   = 303
	songAzul          = 304
	songAzulDaCor     = 305
	songSeptember     = 401
	songSatisfaction  = 501
	songXanadu        = 502
	song1999          = 503

	artistBeatles = 1
	artistRush    = 2
	artistChico   = 3
	artistEarth   = 4
	artistQueen   = 5

	fixtureSongs   = 13
	fixtureSingers = 5
)

// fixtureSQL is a small catalogue with the device's schema: the columns this
// package reads, filled the way the device fills them. FirstWord is the
// accent- and punctuation-stripped uppercase title, truncated to 19 characters;
// AlphaIndex is 1..26 for A..Z and 27 for a leading digit.
const fixtureSQL = `
create table TableDBInfo (ProjectName text, ProductID int, DBVersion int);
insert into TableDBInfo values ('MK8513-BR-B', 1, 7);

create table TableLan (LanType int, LanName_EG text);
insert into TableLan values (1, 'English'), (2, 'Portuguese');

create table TableSinger (
	SingerIndex int, SingerName text, FirstWord text, AlphaIndex int, LanType int);
insert into TableSinger values
	(1, 'The Beatles',          'THE BEATLES',         20, 1),
	(2, 'Rush',                 'RUSH',                18, 1),
	(3, 'Chico Buarque',        'CHICO BUARQUE',        3, 2),
	(4, '"Earth, Wind And Fire"', 'EARTH WIND AND FIRE', 5, 1),
	(5, 'Queen',                'QUEEN',               17, 1);

create table TableSong (
	SongIndex int, SongName text, SingerName text, LanType int,
	FirstWord text, AlphaIndex int);
insert into TableSong values
	(101, 'A DAY IN THE LIFE',              'The Beatles',   1, 'A DAY IN THE LIFE',    1),
	(102, 'HEY JUDE',                       'The Beatles',   1, 'HEY JUDE',             8),
	(201, 'TOM SAWYER',                     'Rush',          1, 'TOM SAWYER',          20),
	(202, 'LIMELIGHT',                      'Rush',          1, 'LIMELIGHT',           12),
	(301, 'CORAÇÃO VAGABUNDO',              'Chico Buarque', 2, 'CORACAO VAGABUNDO',    3),
	(302, 'ÁGUA',                           'Chico Buarque', 2, 'AGUA',                 1),
	(303, 'À FLOR DA PELE',                 'Chico Buarque', 2, 'A FLOR DA PELE',       1),
	(304, 'AZUL',                           'Chico Buarque', 2, 'AZUL',                 1),
	(305, 'AZUL DA COR DO MAR',             'Chico Buarque', 2, 'AZUL DA COR DO MAR',   1),
	(401, 'SEPTEMBER',                      '"Earth, Wind And Fire"', 1, 'SEPTEMBER',  19),
	(501, '(I CAN''T GET NO) SATISFACTION', 'Queen',         1, 'I CANT GET NO SATIS',  9),
	(502, 'XANADU',                         'Queen',         1, 'XANADU',              24),
	(503, '1999',                           'Queen',         1, '1999',                27);

create table TableSongSinger (SongIndex int, SingerIndex int);
insert into TableSongSinger values
	(101, 1), (102, 1), (201, 2), (202, 2),
	(301, 3), (302, 3), (303, 3), (304, 3), (305, 3),
	(401, 4), (501, 5), (502, 5), (503, 5);
`

// writeFixture creates the fixture database and returns its path. The page
// size is small so the file spans several pages, which the reassembly and
// corruption tests need.
func writeFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "song.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	require.NoError(t, err)
	_, err = db.Exec(`pragma page_size = 512; pragma journal_mode = delete;` + fixtureSQL)
	require.NoError(t, err)
	// Padding, so TableSongSinger is not the only thing on the last pages.
	_, err = db.Exec(`create table Padding (Data blob)`)
	require.NoError(t, err)
	for range 20 {
		_, err = db.Exec(`insert into Padding values (randomblob(400))`)
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())
	return path
}

// openFixture opens the fixture as the app would.
func openFixture(t *testing.T) *DB {
	t.Helper()
	db, err := Open(writeFixture(t))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

// readFixture returns the fixture database's bytes.
func readFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(writeFixture(t))
	require.NoError(t, err)
	return b
}
