import { createHash, randomBytes } from 'node:crypto';
import { mkdir, rename, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';

const REAL_FILE_SYSTEM = { mkdir, rename, rm, writeFile };

// Output is named after the page's address, not its body. Two addresses can serve one body,
// and what the extractor makes of it depends on the address it is given as the base for
// links, so a body's hash does not identify a derived page.
export function outputNameOf(address) {
  return createHash('sha256').update(address, 'utf8').digest('hex');
}

// The first two digits spread the files over 256 directories, as the store does for bodies.
export function outputPathsOf(outDir, address) {
  const name = outputNameOf(address);
  const shard = name.slice(0, 2);
  return {
    markdown: path.join(outDir, 'markdown', shard, `${name}.md`),
    cleanHtml: path.join(outDir, 'clean', shard, `${name}.html`),
  };
}

// Every file is written under a temporary name in its own directory and renamed into place
// only once all of them are written, so a run that dies, or a disk that fills, leaves
// finished files and nothing half-written under a final name. The file system is a
// parameter so a test can make a step fail at a chosen moment.
export async function writeFilesAtomically(files, fileSystem = REAL_FILE_SYSTEM) {
  const staged = [];
  try {
    for (const { path: finalPath, content } of files) {
      const directory = path.dirname(finalPath);
      const temporaryPath = path.join(directory, `.incoming-${randomBytes(8).toString('hex')}`);
      staged.push({ temporaryPath, finalPath });
      await fileSystem.mkdir(directory, { recursive: true });
      await fileSystem.writeFile(temporaryPath, content, 'utf8');
    }
    for (const { temporaryPath, finalPath } of staged) {
      await fileSystem.rename(temporaryPath, finalPath);
    }
  } catch (error) {
    await Promise.all(staged.map(({ temporaryPath }) => fileSystem.rm(temporaryPath, { force: true })));
    throw error;
  }
}
