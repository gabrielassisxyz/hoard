// Derives clean HTML and Markdown from stored pages. It reads the lines `hoard list --json`
// prints on standard input, and never touches the network: everything it needs is in the
// store, so it can be run again whenever the cleaning improves.
//
//   hoard list --store <dir> --json | node markdown-pass/derive.mjs --out <dir>
//
// Exit status: 0 when every line was dealt with, 1 when any page or line failed, 2 when the
// command line could not be read.
import { createInterface } from 'node:readline';
import { parseArgs } from 'node:util';
import { deriveEveryLine, summaryLineOf } from './lib/run.mjs';

const USAGE = 'usage: node derive.mjs --out <dir> < pages.jsonl';
const EXIT_USAGE = 2;

function readCommandLine(argv) {
  try {
    const { values } = parseArgs({
      args: argv,
      allowPositionals: false,
      options: {
        out: { type: 'string' },
        help: { type: 'boolean', short: 'h' },
      },
    });
    return { outDir: values.out, help: values.help === true };
  } catch (error) {
    return { problem: error.message };
  }
}

function endWithUsage(problem) {
  console.error(`derive: ${problem}`);
  console.error(USAGE);
  process.exitCode = EXIT_USAGE;
}

const commandLine = readCommandLine(process.argv.slice(2));
if (commandLine.problem) {
  endWithUsage(commandLine.problem);
} else if (commandLine.help) {
  console.log(`${USAGE}\n\nEach input line is one object printed by: hoard list --store <dir> --json`);
} else if (!commandLine.outDir) {
  endWithUsage('--out is required');
} else {
  const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });
  const counts = await deriveEveryLine(lines, commandLine.outDir);
  console.log(summaryLineOf(counts));
  process.exitCode = counts.failed === 0 ? 0 : 1;
}
