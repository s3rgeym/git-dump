package gitindex

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"
)

// GitIndexEntry represents a single entry in the Git index.
type GitIndexEntry struct {
	Ctime    time.Time // Время создания файла
	Mtime    time.Time // Время последнего изменения файла
	Dev      uint32    // Устройство, на котором находится файл
	Ino      uint32    // Номер индекса файла
	Mode     uint32    // Режим доступа к файлу
	Uid      uint32    // Идентификатор пользователя
	Gid      uint32    // Идентификатор группы
	Size     uint32    // Размер файла
	Sha1     string    // SHA-1 хэш объекта
	Flags    uint16    // Флаги записи
	FileName string    // Имя файла
}

type GitIndex struct {
	Version uint32
	Entries []*GitIndexEntry
}

// ParseGitIndex reads the Git index file and returns a list of entries.
func ParseGitIndex(fileName string) (GitIndex, error) {
	index := GitIndex{}
	r, err := os.Open(fileName)
	if err != nil {
		return index, err
	}

	// Read the magic number
	var magic [4]byte
	if err := binary.Read(r, binary.BigEndian, &magic); err != nil {
		return index, fmt.Errorf("failed to read magic number: %w", err)
	}
	if string(magic[:]) != "DIRC" {
		return index, fmt.Errorf("invalid magic number: expected 'DIRC', got '%s'", string(magic[:]))
	}

	// Read the version
	if err := binary.Read(r, binary.BigEndian, &index.Version); err != nil {
		return index, fmt.Errorf("failed to read version: %w", err)
	}
	if index.Version <= 1 || index.Version > 4 {
		return index, fmt.Errorf("unsupported version: %d", index.Version)
	}

	// Read the number of entries
	var numEntries uint32
	if err := binary.Read(r, binary.BigEndian, &numEntries); err != nil {
		return index, fmt.Errorf("failed to read number of entries: %w", err)
	}

	// Read each entry
	// entries := make([]GitIndexEntry, numEntries)
	for i := uint32(0); i < numEntries; i++ {
		entry, err := readGitEntry(r, index.Version)
		if err != nil {
			return index, fmt.Errorf("failed to read entry %d: %w", i, err)
		}
		index.Entries = append(index.Entries, entry)
	}

	return index, nil
}

// readGitEntry reads a single Git index entry from the provided reader.
func readGitEntry(r io.Reader, version uint32) (*GitIndexEntry, error) {
	entry := &GitIndexEntry{}

	// Read the ctime (creation time)
	var ctimeSec uint32
	var ctimeNsec uint32
	if err := binary.Read(r, binary.BigEndian, &ctimeSec); err != nil {
		return nil, fmt.Errorf("failed to read ctime seconds: %w", err)
	}
	if err := binary.Read(r, binary.BigEndian, &ctimeNsec); err != nil {
		return nil, fmt.Errorf("failed to read ctime nanoseconds: %w", err)
	}
	entry.Ctime = time.Unix(int64(ctimeSec), int64(ctimeNsec))

	// Read the mtime (modification time)
	var mtimeSec uint32
	var mtimeNsec uint32
	if err := binary.Read(r, binary.BigEndian, &mtimeSec); err != nil {
		return nil, fmt.Errorf("failed to read mtime seconds: %w", err)
	}
	if err := binary.Read(r, binary.BigEndian, &mtimeNsec); err != nil {
		return nil, fmt.Errorf("failed to read mtime nanoseconds: %w", err)
	}
	entry.Mtime = time.Unix(int64(mtimeSec), int64(mtimeNsec))

	// Read the dev, ino, mode, uid, gid, and size
	if err := binary.Read(r, binary.BigEndian, &entry.Dev); err != nil {
		return nil, fmt.Errorf("failed to read dev: %w", err)
	}
	if err := binary.Read(r, binary.BigEndian, &entry.Ino); err != nil {
		return nil, fmt.Errorf("failed to read ino: %w", err)
	}
	if err := binary.Read(r, binary.BigEndian, &entry.Mode); err != nil {
		return nil, fmt.Errorf("failed to read mode: %w", err)
	}
	if err := binary.Read(r, binary.BigEndian, &entry.Uid); err != nil {
		return nil, fmt.Errorf("failed to read uid: %w", err)
	}
	if err := binary.Read(r, binary.BigEndian, &entry.Gid); err != nil {
		return nil, fmt.Errorf("failed to read gid: %w", err)
	}
	if err := binary.Read(r, binary.BigEndian, &entry.Size); err != nil {
		return nil, fmt.Errorf("failed to read size: %w", err)
	}

	// Read the object ID (20 bytes)
	var objectID [20]byte
	if err := binary.Read(r, binary.BigEndian, &objectID); err != nil {
		return nil, fmt.Errorf("failed to read object ID: %w", err)
	}
	entry.Sha1 = hex.EncodeToString(objectID[:])

	// Read the flags (2 bytes)
	var flags uint16
	if err := binary.Read(r, binary.BigEndian, &flags); err != nil {
		return nil, fmt.Errorf("failed to read flags: %w", err)
	}
	entry.Flags = flags

	extended := flags&0x4000 != 0
	nameLen := flags & 0xFFF
	var extendedFlags uint8

	entryLen := 62
	if extended && version > 2 {
		if err := binary.Read(r, binary.BigEndian, &extendedFlags); err != nil {
			return nil, fmt.Errorf("failed to read extended flags: %w", err)
		}
		entryLen += 1
	}

	if nameLen < 0xFFF {
		fileNameBytes := make([]byte, nameLen)
		if _, err := io.ReadFull(r, fileNameBytes); err != nil {
			return nil, fmt.Errorf("failed to read file name: %w", err)
		}
		entry.FileName = string(fileNameBytes)
		entryLen += int(nameLen)
	} else {
		// Читаем пока не встретим NULL-байт
		fileNameBytes := make([]byte, 0, 256)
		for {
			var b byte
			if err := binary.Read(r, binary.BigEndian, &b); err != nil {
				return nil, fmt.Errorf("failed to read file name byte: %w", err)
			}
			entryLen += 1
			if b == 0 {
				break
			}
			fileNameBytes = append(fileNameBytes, b)
		}
		entry.FileName = string(fileNameBytes)
	}

	// Calculate padding
	padding := 8 - (entryLen % 8)
	if padding == 0 {
		padding = 8
	}

	// Skip padding
	if _, err := io.CopyN(io.Discard, r, int64(padding)); err != nil {
		return nil, fmt.Errorf("failed to skip padding: %w", err)
	}

	return entry, nil
}
