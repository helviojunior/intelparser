package runner


// Fragment contains the data to be scanned
type Fragment struct {
    // Raw is the raw content of the fragment
    Raw string

    Bytes []byte

    // FilePath is the path to the file if applicable
    FilePath    string
    SymlinkFile string

    // CommitSHA is the SHA of the commit if applicable
    CommitSHA string

    // newlineIndices is a list of indices of newlines in the raw content.
    // This is used to calculate the line location of a finding
    newlineIndices [][]int
}