import React, { useEffect, useRef, useState } from "react";
import { api, uploadFile, native, nativeAvailable } from "./api";
import { Modal } from "./views";
import { useTranslation } from "./interface-language.mjs";
import { localizeBackendMessage } from "./backend-message.mjs";

export function BrowserUploads({ requests = [], onStatus }) {
  const tr = useTranslation();
  const [hidden, setHidden] = useState("");
  const request = requests.find((item) => !item.directory && item.id !== hidden && (!item.kind || item.kind === "chooser"));
  const [progress, setProgress] = useState("");
  const [error, setError] = useState("");
  const [percent, setPercent] = useState(0);
  const transfer = useRef();
  useEffect(() => {
    setProgress("");
    setError("");
    return () => transfer.current?.abort();
  }, [request?.id]);
  if (!request) return null;
  const target = request.target || "browser";
  const action = (name) => target === "browser" ? `/browser/upload-${name}` : `/input/files/${name}`;
  const body = { id: request.id, ...(target === "browser" ? {} : { target }) };
  const cancel = () => {
    transfer.current?.abort();
    setHidden(request.id);
    void api(action("cancel"), body).catch(() => {});
  };
  const select = async (files) => {
    if (!files.length) return;
    if (
      files.length > (request.multiple ? 100 : 1) ||
      files.some((file) => file.size > 2 * 1024 ** 3)
    ) {
      setError("每个文件不能超过 2 GB；多选最多 100 个文件。");
      return;
    }
    const controller = new AbortController();
    transfer.current = controller;
    setError("");
    try {
      for (let i = 0; i < files.length; i++) {
        const file = files[i];
        setProgress(tr("正在传递 {0}/{1}：{2}", i + 1, files.length, file.name));
        setPercent(0);
        await uploadFile(
          `/input/files/${target}/${request.id}?${new URLSearchParams({ name: file.name })}`,
          file,
          {
            signal: controller.signal,
            onProgress: (loaded, total) =>
              setPercent(
                total ? Math.min(100, Math.floor((loaded * 100) / total)) : 100,
              ),
          },
        );
      }
      setProgress("正在交给应用…");
      await api(action("finish"), body);
      onStatus("所选文件已交给应用，请在应用中确认上传结果");
    } catch (error) {
      if (!controller.signal.aborted) {
        setError(error.message);
        setProgress("");
        // A failed batch must not retain half of the selected files.
        await api(action("cancel"), body).catch(() => {});
        onStatus(tr("{0}；请重新点击上传按钮", localizeBackendMessage(error.message, tr)));
      }
    }
  };
  const choose = async () => {
    setError("");
    try {
      const selection = await native("chooseUploadFiles", { multiple: request.multiple });
      if (selection.canceled) { cancel(); return; }
      setProgress("正在传递所选文件…");
      await native("uploadHostFiles", { target, id: request.id, selection: selection.fileSelection });
      await api(action("finish"), body);
      setHidden(request.id);
      onStatus("所选文件已交给应用，请在应用中确认上传结果");
    } catch (error) {
      setError(error.message); setProgress("");
      await api(action("cancel"), body).catch(() => {});
      onStatus(tr("{0}；请重新点击上传按钮", localizeBackendMessage(error.message, tr)));
    }
  };
  return (
    <Modal
      title={tr("上传本机文件")}
      onClose={cancel}
      footer={<button onClick={cancel}>{tr("取消上传")}</button>}
    >
      <div className="modal-body browser-upload-body">
        <p>
          {tr("选择要上传的图片、视频或文档。仅传递所选文件，目标网页或应用负责处理附件。")}
        </p>
        <p className="field-help">{tr("支持的格式自动清理定位、时区等元数据。无法安全清理时，可在设置→通用中明确开启“允许未支持格式原样传递”；原样文件可能保留元数据。")}</p>
        {nativeAvailable() ? <button className="primary" disabled={Boolean(progress)} onClick={() => void choose()}>{tr("选择本机文件")}</button> : <label className="field">
          {tr("选择本机文件")}
          <input
            type="file"
            multiple={request.multiple}
            disabled={Boolean(progress)}
            onChange={(event) =>
              void select(Array.from(event.target.files || []))
            }
          />
        </label>}
        {progress && (
          <>
            <p role="status">
              {tr(progress)}{nativeAvailable() ? "" : ` · ${percent}%`}
            </p>
            <progress aria-label={tr("文件传递进度")} max="100" value={nativeAvailable() ? undefined : percent} />
          </>
        )}
        {error && <p role="alert">{localizeBackendMessage(error, tr)}</p>}
      </div>
    </Modal>
  );
}
