// errand::step, errand::view, errand::result: what a run says to errand ( ADR 0032 ), header-only, C++17.
//
//     #include <errand/say.hpp>
//
//     errand::step( "iteration 41", 0.41, { { "residual", 3e-7 } } );   // where it is, how far, its curves
//     errand::view( "convergence.html", "Convergence" );                // a file of the run worth looking at
//     errand::result( "seconds", 1.52 );                                // a number kept in its result.yaml
//     std::string dir = errand::out_dir();                              // the run's own directory ( "out" by hand )
//
// Each is one line of JSON appended to a file errand names in the run's environment ( `ERRAND_STEPS`,
// `ERRAND_RESULTS` ). Outside errand nothing is written and nothing fails: the program runs the same by hand.
// Its include directory is `{errand_include}` in a runner's build line.
#pragma once

#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <initializer_list>
#include <sstream>
#include <string>
#include <vector>
#include <utility>

namespace errand {

namespace say_detail {
    inline std::string env( const char *name ) {
        const char *v = std::getenv( name );
        return v ? v : "";
    }

    inline std::string quoted( const std::string &s ) {
        std::string out = "\"";
        for ( unsigned char c : s ) {
            if ( c == '"' || c == '\\' ) { out += '\\'; out += char( c ); }
            else if ( c == '\n' ) out += "\\n";
            else if ( c == '\t' ) out += "\\t";
            else if ( c < 0x20 ) { char b[ 8 ]; std::snprintf( b, sizeof b, "\\u%04x", c ); out += b; }
            else out += char( c );
        }
        return out + "\"";
    }

    inline std::string number( double v ) {
        std::ostringstream o;
        o.precision( 17 );
        o << v;
        std::string s = o.str();
        return ( s == "inf" || s == "-inf" || s == "nan" || s == "-nan" ) ? "null" : s;      // not JSON: left out as null
    }

    inline double now() {
        using namespace std::chrono;
        return duration< double >( system_clock::now().time_since_epoch() ).count();
    }

    /// One line at the end of the file named by `var`, in one write: lines of several writers never mix.
    inline bool append( const char *var, const std::string &line ) {
        std::string path = env( var );
        if ( path.empty() )
            return false;
        std::FILE *f = std::fopen( path.c_str(), "ab" );
        if ( ! f )
            return false;
        std::string whole = line + "\n";
        std::fwrite( whole.data(), 1, whole.size(), f );
        std::fclose( f );
        return true;
    }
}

/// Whether errand runs this program ( `ERRAND_PROTOCOL` ): outside, a view says where it is, and every other call below does nothing.
inline bool inside() { return ! say_detail::env( "ERRAND_PROTOCOL" ).empty(); }

/// Where the run is: `text`, how far ( `done`, from 0 to 1; negative: not said ), and its curves, by name.
inline void step( const std::string &text, double done = -1, std::initializer_list< std::pair< const char *, double > > curves = { } ) {
    std::string line = "{\"t\": " + say_detail::number( say_detail::now() );
    if ( ! text.empty() ) line += ", \"text\": " + say_detail::quoted( text );
    if ( done >= 0 ) line += ", \"done\": " + say_detail::number( done );
    for ( const auto &c : curves ) line += ", " + say_detail::quoted( c.first ) + ": " + say_detail::number( c.second );
    say_detail::append( "ERRAND_STEPS", line + "}" );
}

/// A file ( or a directory ) of the run worth looking at as it evolves: `path` from the run's directory. Write the
/// file first, whole ( beside, then renamed ); name it again when it changed.
inline void view( const std::string &path, const std::string &title = { } ) {
    std::string line = "{\"t\": " + say_detail::number( say_detail::now() ) + ", \"view\": " + say_detail::quoted( path );
    if ( ! title.empty() ) line += ", \"title\": " + say_detail::quoted( title );
    if ( say_detail::append( "ERRAND_STEPS", line + "}" ) )
        return;                                     // errand says where it opens
    static std::vector< std::string > shown;        // run by hand: said here, once
    for ( const std::string &s : shown )
        if ( s == path )
            return;
    shown.push_back( path );
    std::fprintf( stderr, "  -> %s  %s\n", title.empty() ? path.c_str() : title.c_str(), path.c_str() );
}

/// A number, or a word, kept in the run's `result.yaml` under `results:`.
inline void result( const std::string &name, double value ) {
    say_detail::append( "ERRAND_RESULTS", "{" + say_detail::quoted( name ) + ": " + say_detail::number( value ) + "}" );
}
inline void result( const std::string &name, const std::string &value ) {
    say_detail::append( "ERRAND_RESULTS", "{" + say_detail::quoted( name ) + ": " + say_detail::quoted( value ) + "}" );
}
inline void result( const std::string &name, const char *value ) { result( name, std::string( value ) ); }

/// The run's own directory, when one was asked for ( `-o` ); `out` otherwise, as by hand.
inline std::string out_dir() {
    std::string d = say_detail::env( "ERRAND_OUT_DIR" );
    return d.empty() ? "out" : d;
}

/// What a program exits with to say it was skipped, not failed.
constexpr int SKIP = 77;

}
