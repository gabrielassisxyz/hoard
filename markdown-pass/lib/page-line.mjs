// One line of `hoard list --store <dir> --json`, as this pass reads it. Only these keys are
// the contract between the two programs; keys a later version adds are left alone.
const STRING_KEYS = ['address', 'final_address', 'fetched_at', 'content_type', 'sha256', 'body_path'];
const INTEGER_KEYS = ['http_status', 'size_bytes'];

// Returns { page } for a usable line and { problem } for any other, so that one bad line is
// a line in the report and not the end of the run.
export function parsePageLine(line) {
  let value;
  try {
    value = JSON.parse(line);
  } catch (error) {
    return { problem: `not valid JSON: ${error.message}` };
  }
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return { problem: 'not a JSON object' };
  }
  const unusable = [
    ...STRING_KEYS.filter((key) => typeof value[key] !== 'string'),
    ...INTEGER_KEYS.filter((key) => !Number.isInteger(value[key])),
  ];
  if (unusable.length > 0) {
    return { problem: `missing or mistyped keys: ${unusable.join(', ')}` };
  }
  return { page: value };
}
