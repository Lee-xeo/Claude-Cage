import { api } from "./api";
import { runtimeSocketURL } from "./client-context.mjs";
import { AudioInput as BaseAudioInput } from "./audio-input-controller.mjs";
import workletURL from "./audio-input-worklet.js?url&no-inline";

export class AudioInput extends BaseAudioInput {
  constructor(onState, onError, deps = {}) {
    super(onState, onError, {
      api,
      socketURL: runtimeSocketURL,
      workletURL,
      ...deps,
    });
  }
}
