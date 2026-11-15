// Derives Markdown from stored pages. It reads the lines `hoard list --json` prints on
// standard input, and never touches the network: everything it needs is in the store, so it
// can be run again whenever the cleaning improves.
//
//   hoard list --store <dir> --json | node markdown-pass/derive.mjs --out <dir>
//
// With --clean-html it also writes the cleaned HTML the Markdown was made from.
//
// Keeping a capture's images takes three steps. --list-images prints the address of every
// image in the pages' content and writes nothing; `hoard assets` fetches them; and a second
// run with --assets <file>, the lines `hoard list --assets --json` prints, writes Markdown
// whose images point at the copies the store holds.
//
//   hoard list --store <dir> --json | node markdown-pass/derive.mjs --list-images > images.txt
//   hoard assets --store <dir> --list images.txt
//   hoard list --store <dir> --json > pages.jsonl
//   hoard list --store <dir> --assets --json > assets.jsonl
//   node markdown-pass/derive.mjs --out <dir> --assets assets.jsonl < pages.jsonl
//
// Exit status: 0 when every line was dealt with, 1 when any page or line failed, 2 when the
// command line or the --assets file could not be read.
import { readFile } from 'node:fs/promises';
import { createInterface } from 'node:readline';
import { parseArgs } from 'node:util';
import { parseAssetLines } from './lib/images.mjs';
import { deriveEveryLine, listImagesOfEveryLine, summaryLineOf } from './lib/run.mjs';

const USAGE = [
  'usage: node derive.mjs --out <dir> [--clean-html] [--assets <file>] < pages.jsonl',
  '       node derive.mjs --list-images < pages.jsonl',
].join('\n');
const EXIT_USAGE = 2;

function readCommandLine(argv) {
  try {
    const { values } = parseArgs({
      args: argv,
      allowPositionals: false,
      options: {
        out: { type: 'string' },
        'clean-html': { type: 'boolean' },
        'list-images': { type: 'boolean' },
        assets: { type: 'string' },
        help: { type: 'boolean', short: 'h' },
      },
    });
    return {
      outDir: values.out,
      cleanHtml: values['clean-html'] === true,
      listImages: values['list-images'] === true,
      assetsFile: values.assets,
      help: values.help === true,
    };
  } catch (error) {
    return { problem: error.message };
  }
}

function endWithUsage(problem) {
  console.error(`derive: ${problem}`);
  console.error(USAGE);
  process.exitCode = EXIT_USAGE;
}

// The problem with the options taken together, if there is one. --list-images writes no
// file, so an option that shapes a file written is a mistake, not something to ignore.
function problemWithOptions({ outDir, cleanHtml, listImages, assetsFile }) {
  if (listImages) {
    return outDir || cleanHtml || assetsFile ? '--list-images writes no file and takes none of --out, --clean-html or --assets' : null;
  }
  return outDir ? null : '--out is required';
}

// The map from address to image file of the --assets file, or null once every problem with
// it is on standard error. It is read before any page, so a bad line leaves no Markdown.
async function readAssets(assetsFile) {
  let text;
  try {
    text = await readFile(assetsFile, 'utf8');
  } catch (error) {
    console.error(`derive: cannot read --assets ${assetsFile}: ${error.message}`);
    return null;
  }
  const { files, problems } = parseAssetLines(text);
  problems.forEach((problem) => console.error(`derive: --assets ${assetsFile}: ${problem}`));
  return problems.length === 0 ? files : null;
}

// Made only once the first await is behind: lines read from standard input before a loop is
// iterating them are dropped, and reading the --assets file takes an await.
function standardInputLines() {
  return createInterface({ input: process.stdin, crlfDelay: Infinity });
}

async function listImages() {
  const lines = standardInputLines();
  const { addresses, failed } = await listImagesOfEveryLine(lines);
  addresses.forEach((address) => console.log(address));
  process.exitCode = failed === 0 ? 0 : 1;
}

async function deriveAll(assets, { outDir, cleanHtml }) {
  const lines = standardInputLines();
  const counts = await deriveEveryLine(lines, outDir, { cleanHtml, assets });
  console.log(summaryLineOf(counts));
  process.exitCode = counts.failed === 0 ? 0 : 1;
}

async function runPass(commandLine) {
  if (commandLine.listImages) {
    return listImages();
  }
  const assets = commandLine.assetsFile === undefined ? new Map() : await readAssets(commandLine.assetsFile);
  if (assets === null) {
    process.exitCode = EXIT_USAGE;
    return undefined;
  }
  return deriveAll(assets, commandLine);
}

const commandLine = readCommandLine(process.argv.slice(2));
const optionsProblem = commandLine.problem ?? (commandLine.help ? null : problemWithOptions(commandLine));
if (optionsProblem) {
  endWithUsage(optionsProblem);
} else if (commandLine.help) {
  console.log(`${USAGE}\n\nEach input line is one object printed by: hoard list --store <dir> --json`);
} else {
  await runPass(commandLine);
}
