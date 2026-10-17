"""T001/REQ-001、020：只读核验G1基线与G3九项指纹；不签发审批。"""
import hashlib
import json
import os
from pathlib import Path

root = Path(os.environ.get('LIYUANCI_DOCS_DIR', '../docs'))
expected = {
 'requirements/baseline.md':'fed56e2811cd21c5c5d461a35d9ace525b6f9bc142a01a6a1f2048303da54810',
 'design/options.md':'6eec4f22ddb08482d1dc13c601716cdb314d556d8054522286eb65e5058f7375',
 'design/architecture.md':'d6f66b1f2c816adde3899dda7d32f7c731444ee7b1e7186eeb091adaf20d8e50',
 'design/database.md':'29d3497d2ec9ebe7d1bda7097dee937e71c5495b400890b1336c4b54636063cf',
 'design/api.md':'9a725420318d402ad1cea3229ed4b09edbe55c9376055e4d4cc280203ab7f142',
 'design/security.md':'5cebf31886998649a80e75d7d79cce34ed91537b8441beba09959c6f7d5ede88',
 'design/decisions.md':'8fe78bc027e1ff62dc4bf8dcf093b386d2601114695aca7720225c0f5c5f50b9',
 'design/performance.md':'d580049ef5994efcd250c70213294706b2f43ba82b9c737f107b7d91c65017a4',
 'reviews/architecture-review.md':'de6da91449cd906e4829480664ed763dd21ee0468a60ab5a45f3fbac70706d32',
 'reviews/architecture-remediation.md':'14b17499fc1dd3845b83b69987d15fc0e6364d59082a9b865efbfae9727f63ef',
}
for name, digest in expected.items():
 actual=hashlib.sha256((root/name).read_bytes()).hexdigest()
 if actual!=digest:raise SystemExit('T001 FAILED: '+name)
print(json.dumps({'task_id':'T001','req_ids':['REQ-001','REQ-020'],'result':'PASS','baseline':'v0.2','design_snapshot':'ff882ab0c8bdb59d3509e0782c7b13ec77e5c7e4','sha256':expected},ensure_ascii=False,indent=2))
