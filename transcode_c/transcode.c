#include <stdio.h>
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>

void print_error(char message[])
{
	fprintf(stderr, "%s\n", message);
}

struct cleanup {
	AVFormatContext **input_format_context;
	AVFormatContext **output_format_context;
	AVCodecContext **output_codec_context;
};

void cleanup(char *message, int ret, struct cleanup *cu) {
	if (*cu->input_format_context)
		avformat_close_input(cu->input_format_context);

	if (*cu->output_format_context) {
		if ((*cu->output_format_context)->pb)
			avio_closep(&(*cu->output_format_context)->pb);

		avformat_free_context(*cu->output_format_context);
	}

	if (message)
		fputs(message, stderr);

	exit(ret);
}

int main(int argc, char *argv[])
{
	struct cleanup cu;

	if (argc < 3)
		cleanup("input/output_files not specified", 1, &cu);

	char *in_filename = argv[1];
	char *out_filename = argv[2];
	av_register_all();
	AVFormatContext *input_format_context = NULL;
	cu.input_format_context = &input_format_context;
	int error;

	if ((error = avformat_open_input(&input_format_context, in_filename, NULL, NULL)) < 0)
		cleanup("avformat_open_input", error, &cu);

	if ((error = avformat_find_stream_info(input_format_context, NULL)) < 0)
		cleanup("avformat_find_stream_info", error, &cu);

	if ((input_format_context)->nb_streams != 1)
		cleanup("more than one audio stream in file", 1, &cu);

	AVCodecContext *input_codec_context = input_format_context->streams[0]->codec;
	AVCodec *input_codec;

	if (!(input_codec = avcodec_find_decoder(input_codec_context->codec_id)))
		cleanup("could not find input codec", 1, &cu);

	if ((error = avcodec_open2(input_codec_context, input_codec, NULL)) < 0) {
		cleanup("avcodec_open2", error, &cu);
	}

	AVIOContext *output_io_context = NULL;

	if ((error = avio_open(&output_io_context, out_filename, AVIO_FLAG_WRITE)) < 0)
		cleanup("avio_open", error, &cu);

	AVFormatContext *output_format_context;
	cu.output_format_context = &output_format_context;

	if (!(output_format_context = avformat_alloc_context()))
		cleanup("could not allocate output format context", 1, &cu);

	output_format_context->pb = output_io_context;

	if (!(output_format_context->oformat = av_guess_format(NULL, out_filename, NULL)))
		cleanup("could not find output file format", 1, &cu);

	av_strlcpy(output_format_context->filename, out_filename, sizeof(output_format_context->filename));
	AVCodec *output_codec = NULL;

	if (!(output_codec = avcodec_find_encoder(AV_CODEC_ID_OPUS)))
		cleanup("could not find an Opus encoder", 1, &cu);

	AVStream *stream = NULL;

	if (!(stream = avformat_new_stream(output_format_context, output_codec)))
		cleanup("could not create a new stream", 1, &cu);

	AVCodecContext *output_codec_context = stream->codec;
	cu.output_codec_context = &output_codec_context;

	return 0;
 }